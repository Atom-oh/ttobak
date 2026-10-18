package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecsTypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/transcribe"
	"golang.org/x/text/unicode/norm"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

var (
	transcribeService *service.TranscribeService
	dictService       *service.DictionaryService
	repo              *repository.DynamoDBRepository
	ecsClient         ecsAPI
	s3Client          *s3.Client
	outputBucketName  string
	whisperCluster    string
	whisperTaskDef    string
	whisperContainer  string
)

func init() {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("failed to load AWS config: %v", err)
	}

	dynamoClient := dynamodb.NewFromConfig(cfg)
	s3Client0 := s3.NewFromConfig(cfg)
	transcribeClient := transcribe.NewFromConfig(cfg)

	tableName := os.Getenv("TABLE_NAME")
	if tableName == "" {
		tableName = "ttobak-main"
	}
	outputBucket := os.Getenv("BUCKET_NAME")
	if outputBucket == "" {
		outputBucket = "ttobak-assets"
	}

	repo = repository.NewDynamoDBRepositoryWithS3(dynamoClient, tableName, s3Client0, outputBucket)
	transcribeService = service.NewTranscribeService(transcribeClient, s3Client0, repo, outputBucket)

	dictRepo := repository.NewDictionaryRepository(dynamoClient, tableName)
	dictService = service.NewDictionaryService(dictRepo, transcribeClient)

	ecsClient = ecs.NewFromConfig(cfg)
	s3Client, outputBucketName = s3Client0, outputBucket
	whisperCluster = os.Getenv("WHISPER_CLUSTER")
	whisperTaskDef = os.Getenv("WHISPER_TASK_DEF")
	whisperContainer = os.Getenv("WHISPER_CONTAINER")
	if whisperContainer == "" {
		whisperContainer = "whisper"
	}
}

// Handler processes EventBridge S3 events for new audio uploads
func Handler(ctx context.Context, raw json.RawMessage) error {
	var envelope struct {
		Source     string          `json:"source"`
		DetailType string          `json:"detail-type"`
		Detail     json.RawMessage `json:"detail"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	if envelope.Source == sttRecoverySource && envelope.DetailType == sttRecoveryDetailType {
		return runSttRecoveryTick(ctx)
	}
	if envelope.Source == "ttobak.audio" && envelope.DetailType == "AudioCropRequested" {
		var detail struct {
			UserID    string `json:"userId"`
			MeetingID string `json:"meetingId"`
		}
		if err := json.Unmarshal(envelope.Detail, &detail); err != nil {
			return err
		}
		if detail.UserID == "" || detail.MeetingID == "" || strings.ContainsAny(detail.UserID+detail.MeetingID, "/\\") {
			return fmt.Errorf("invalid crop identity")
		}
		meeting, err := repo.MetadataView().GetMeeting(ctx, detail.UserID, detail.MeetingID)
		if err != nil {
			return err
		}
		if meeting == nil || !meeting.AudioCrop.QueuedFor(meeting.Status) {
			return nil
		}
		if whisperCluster == "" || whisperTaskDef == "" || whisperContainer != "whisper" {
			return fmt.Errorf("audio cropping requires the production Whisper worker")
		}
		return startWhisperTask(ctx, detail.MeetingID, detail.UserID, "", "", "", len(meeting.Participants),
			whisperTaskOptions{Crop: true, ClientToken: "crop-" + detail.MeetingID})
	}
	var event model.EventBridgeS3Event
	if err := json.Unmarshal(raw, &event); err != nil {
		return fmt.Errorf("failed to unmarshal EventBridge event: %w", err)
	}

	bucket := event.Detail.Bucket.Name
	key := event.Detail.Object.Key

	// URL decode the key (handles both + and %XX encoding)
	if decoded, err := url.QueryUnescape(key); err == nil {
		key = decoded
	}
	// macOS stores filenames in NFD (decomposed jamo); ECS RunTask rejects non-NFC env vars
	key = norm.NFC.String(key)

	log.Printf("Processing S3 event: bucket=%s, key=%s", bucket, key)

	// Only process audio files
	if !strings.HasPrefix(key, "audio/") {
		log.Printf("Skipping non-audio file: %s", key)
		return nil
	}

	// Skip checkpoint files (periodic saves during recording, not final audio)
	if strings.Contains(key, "checkpoint_") {
		log.Printf("Skipping checkpoint file: %s", key)
		return nil
	}

	// Skip progress files (cumulative checkpoint for crash recovery, not final audio)
	if strings.Contains(key, "recording_progress") {
		log.Printf("Skipping progress file: %s", key)
		return nil
	}
	if cropResultKeyPattern.MatchString(key) {
		return nil
	}

	if cropResultKeyPattern.MatchString(key) {
		return nil
	}

	// Skip realtime-aggregated audio (already transcribed in realtime by ECS whisper)
	if strings.Contains(key, "realtime_") {
		log.Printf("Skipping realtime audio file (already transcribed): %s", key)
		return nil
	}

	// Extract meeting ID from key
	// Expected format: audio/{userID}/{meetingID}/{filename}
	meetingID := service.ExtractMeetingIDFromAudioKey(key)
	if meetingID == "" {
		log.Printf("Could not extract meeting ID from key: %s", key)
		return nil
	}

	// Detect multi-part upload from S3 key pattern (e.g. part_000_, part_001_)
	partIndex, isMultiPart := extractPartIndex(key)
	var outputKey string
	if isMultiPart {
		outputKey = fmt.Sprintf("transcripts/%s_part_%03d.json", meetingID, partIndex)
		log.Printf("Multi-part audio detected: part %d, output key: %s", partIndex, outputKey)
	}

	log.Printf("Starting transcription for meeting: %s", meetingID)

	// Extract userID from key: audio/{userID}/{meetingID}/{filename}
	parts := strings.Split(key, "/")
	userID := ""
	if len(parts) >= 3 {
		userID = parts[1]
	}

	// Read meeting record to check sttProvider selection and the rediarize
	// speaker-count hint. GetMeeting (PK/SK GetItem) is used over
	// GetMeetingByID (GSI3 Query) whenever userID is known -- GSI reads on
	// DynamoDB can never be ConsistentRead, so a RediarizeMeeting-triggered
	// re-upload landing here before that GSI replica catches up would read a
	// stale DiarizationSpeakerHint/Participants and silently re-run with the
	// old speaker count.
	var meeting *model.Meeting
	var err error
	if userID != "" {
		meeting, err = repo.GetMeeting(ctx, userID, meetingID)
	} else {
		meeting, err = repo.GetMeetingByID(ctx, meetingID)
	}
	if err != nil {
		return fmt.Errorf("read meeting before transcription: %w", err)
	}
	if meeting != nil && meeting.AudioCrop != nil {
		return nil
	}
	if skip, err := recoveryAudioEvent(meeting, key); skip || err != nil {
		return err
	}

	sttProvider := "whisper"
	if meeting != nil && meeting.SttProvider != "" {
		sttProvider = meeting.SttProvider
	}

	// Look up user's custom vocabulary for Transcribe
	var customVocab string
	if userID != "" {
		if vocab, vocabErr := dictService.GetVocabularyForTranscription(ctx, userID); vocabErr == nil && vocab != "" {
			customVocab = vocab
			log.Printf("Using custom vocabulary %s for user %s", vocab, userID)
		}
	}

	var jobName string
	switch sttProvider {
	case "whisper":
		if whisperCluster == "" || whisperTaskDef == "" {
			log.Printf("Whisper not configured, falling back to Transcribe")
			jobName, err = transcribeService.StartTranscriptionJob(ctx, meetingID, bucket, key, outputKey, customVocab)
		} else {
			log.Printf("Using Whisper GPU for meeting: %s", meetingID)
			initialPrompt, numSpeakers := whisperInputs(ctx, meeting, userID)
			err = startWhisperTask(ctx, meetingID, userID, key, initialPrompt, outputKey, numSpeakers, whisperTaskOptions{})
			jobName = "whisper-ecs-" + meetingID
		}
	case "nova-sonic":
		log.Printf("Using Nova Sonic transcription for meeting: %s", meetingID)
		jobName, err = transcribeService.StartNovaSonicTranscription(ctx, meetingID, bucket, key, outputKey)
	default:
		jobName, err = transcribeService.StartTranscriptionJob(ctx, meetingID, bucket, key, outputKey, customVocab)
	}
	if err != nil {
		log.Printf("Failed to start transcription job: %v", err)
		if meeting != nil {
			if statusErr := repo.UpdateMeetingFields(ctx, meeting.UserID, meeting.MeetingID, map[string]interface{}{"status": model.StatusError}); statusErr != nil {
				log.Printf("failed to mark meeting %s as error: %v", meeting.MeetingID, statusErr)
			}
		}
		return fmt.Errorf("failed to start transcription job: %w", err)
	}

	log.Printf("Started transcription job: %s (provider=%s) for meeting: %s", jobName, sttProvider, meetingID)

	return nil
}

// whisperTaskOptions distinguishes ordinary, crop and recovery runs. Only
// container environment overrides are ever set: never entryPoint/command.
type whisperTaskOptions struct {
	Crop        bool
	ClientToken string
	StartedBy   string
}

type ecsAPI interface {
	RunTask(ctx context.Context, params *ecs.RunTaskInput, optFns ...func(*ecs.Options)) (*ecs.RunTaskOutput, error)
	ListTasks(ctx context.Context, params *ecs.ListTasksInput, optFns ...func(*ecs.Options)) (*ecs.ListTasksOutput, error)
}

// whisperInputs derives the ASR inputs shared by the S3-event and recovery
// paths: the user's dictionary as initial_prompt and the max_speakers bound.
func whisperInputs(ctx context.Context, meeting *model.Meeting, userID string) (string, int) {
	var initialPrompt string
	if userID != "" && loadDictionaryTerms != nil {
		if phrases, err := loadDictionaryTerms(ctx, userID); err == nil && len(phrases) > 0 {
			initialPrompt = strings.Join(phrases, ", ")
			log.Printf("Whisper initial_prompt: %d terms for user %s", len(phrases), userID)
		}
	}
	// Participant count is passed through as pyannote's max_speakers
	// upper bound (pyannote still auto-detects the actual count within it --
	// registered headcount can exceed actual speakers, e.g. invited-but-silent
	// attendees); 0 (Participants empty/unset) means "no bound, fully
	// auto-detect".
	numSpeakers := 0
	if meeting != nil {
		numSpeakers = len(meeting.Participants)
		if meeting.DiarizationSpeakerHint > 0 {
			numSpeakers = meeting.DiarizationSpeakerHint
		}
	}
	return initialPrompt, numSpeakers
}

var loadDictionaryTerms = func(ctx context.Context, userID string) ([]string, error) {
	dict, err := dictService.GetDictionary(ctx, userID)
	if err != nil || dict == nil {
		return nil, err
	}
	phrases := make([]string, 0, len(dict.Terms))
	for _, t := range dict.Terms {
		display := t.DisplayAs
		if display == "" {
			display = t.Phrase
		}
		phrases = append(phrases, display)
	}
	return phrases, nil
}

func whisperRunTaskInput(meetingID, userID, audioKey, initialPrompt, outputKey string, numSpeakers int, opts whisperTaskOptions) *ecs.RunTaskInput {
	envOverrides := []ecsTypes.KeyValuePair{
		{Name: aws.String("AUDIO_KEY"), Value: aws.String(audioKey)},
		{Name: aws.String("MEETING_ID"), Value: aws.String(meetingID)},
		{Name: aws.String("USER_ID"), Value: aws.String(userID)},
	}
	if initialPrompt != "" {
		envOverrides = append(envOverrides, ecsTypes.KeyValuePair{
			Name: aws.String("INITIAL_PROMPT"), Value: aws.String(initialPrompt),
		})
	}
	if outputKey != "" {
		envOverrides = append(envOverrides, ecsTypes.KeyValuePair{
			Name: aws.String("OUTPUT_KEY"), Value: aws.String(outputKey),
		})
	}
	if numSpeakers > 0 {
		// Passed to the container as max_speakers -- an upper bound
		// pyannote still auto-detects within, not an exact count (see
		// transcribe.py:_diarize). Omitted (0) means no bound at all.
		envOverrides = append(envOverrides, ecsTypes.KeyValuePair{
			Name: aws.String("NUM_SPEAKERS"), Value: aws.String(strconv.Itoa(numSpeakers)),
		})
	}
	if opts.Crop {
		envOverrides = append(envOverrides, ecsTypes.KeyValuePair{Name: aws.String("AUDIO_CROP"), Value: aws.String("1")})
	}
	input := &ecs.RunTaskInput{
		Cluster:        aws.String(whisperCluster),
		TaskDefinition: aws.String(whisperTaskDef),
		Count:          aws.Int32(1),
		CapacityProviderStrategy: []ecsTypes.CapacityProviderStrategyItem{
			{
				CapacityProvider: aws.String("ttobak-whisper-spot"),
				Weight:           1,
			},
		},
		Overrides: &ecsTypes.TaskOverride{
			ContainerOverrides: []ecsTypes.ContainerOverride{
				{
					Name:        aws.String(whisperContainer),
					Environment: envOverrides,
				},
			},
		},
	}
	if opts.ClientToken != "" {
		input.ClientToken = aws.String(opts.ClientToken)
	}
	if opts.StartedBy != "" {
		input.StartedBy = aws.String(opts.StartedBy)
	}
	return input
}

func startWhisperTask(ctx context.Context, meetingID, userID, audioKey, initialPrompt, outputKey string, numSpeakers int, opts whisperTaskOptions) error {
	result, err := ecsClient.RunTask(ctx, whisperRunTaskInput(meetingID, userID, audioKey, initialPrompt, outputKey, numSpeakers, opts))
	if err != nil {
		return err
	}
	if len(result.Tasks) > 0 && result.Tasks[0].TaskArn != nil {
		log.Printf("ECS task started: %s", *result.Tasks[0].TaskArn)
	}
	if len(result.Failures) > 0 {
		log.Printf("ECS task placement failures: %v", result.Failures)
		return fmt.Errorf("ECS task placement failed: %s", aws.ToString(result.Failures[0].Reason))
	}
	return nil
}

// partIndexPattern matches the multi-part audio prefix only when it appears
// at the START of the S3 object's basename (e.g. `audio/{user}/{meeting}/part_001_xxx.wav`).
// Without the path-anchor a legitimate user-named single upload like
// `part_002_song.wav` would be misclassified as a multi-part chunk,
// routing it to the merge pipeline that would then block forever on
// missing sibling parts.
var partIndexPattern = regexp.MustCompile(`(?:^|/)part_(\d{3})_`)
var cropResultKeyPattern = regexp.MustCompile(`^audio/[^/]+/[^/]+/crop_result_[0-9a-f]{32}\.wav$`)

func extractPartIndex(key string) (int, bool) {
	matches := partIndexPattern.FindStringSubmatch(key)
	if len(matches) < 2 {
		return 0, false
	}
	idx, err := strconv.Atoi(matches[1])
	if err != nil {
		return 0, false
	}
	return idx, true
}

func main() {
	lambda.Start(Handler)
}

// A copied checkpoint can emit its S3 event before RecoverMeeting commits the
// canonical key. Retry that event; discard a copy that lost the state CAS.
var recoveredAudioPattern = regexp.MustCompile(`^audio/[^/]+/[^/]+/recording_recovered_(?:[0-9]+|[0-9a-f]{32})\.(webm|m4a|ogg)$`)

func recoveryAudioEvent(meeting *model.Meeting, key string) (bool, error) {
	if !recoveredAudioPattern.MatchString(key) {
		return false, nil
	}
	if meeting == nil {
		return true, nil
	}
	keys := meeting.GetEffectiveAudioKeys()
	if len(keys) == 0 {
		return false, fmt.Errorf("recovered audio is awaiting its meeting binding")
	}
	return len(keys) != 1 || keys[0] != key, nil
}
