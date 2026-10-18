package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecsTypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

const (
	recUser = "owner-1"
	recM1   = "0b8f8a8e-1234-4abc-9def-0123456789ab"
	recM2   = "1b8f8a8e-1234-4abc-9def-0123456789ab"
)

var recNow = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

func recKey(mid string) string {
	return "audio/" + recUser + "/" + mid + "/1700000000000_비밀회의.webm"
}

func stuckRow(mid string) model.SttRecoveryRow {
	return model.SttRecoveryRow{MeetingID: mid, UserID: recUser, Status: model.StatusError,
		UpdatedAt: recNow.Add(-time.Hour).Format(time.RFC3339Nano), AudioKey: recKey(mid),
		Present: map[string]bool{"updatedAt": true, "audioKey": true}}
}

type fakeRecoveryRepo struct {
	rows      []model.SttRecoveryRow
	refs      map[string]bool
	claims    []string
	summaries []string
	marks     []string
	claimErr  error
}

func (f *fakeRecoveryRepo) ScanSttRecoveryRows(context.Context) ([]model.SttRecoveryRow, error) {
	return f.rows, nil
}
func (f *fakeRecoveryRepo) ClaimSttRecovery(_ context.Context, row model.SttRecoveryRow, key string, attempt int, now time.Time) (string, error) {
	if f.claimErr != nil {
		return "", f.claimErr
	}
	f.claims = append(f.claims, fmt.Sprintf("%s:%d", row.MeetingID, attempt))
	return now.Format(time.RFC3339Nano), nil
}
func (f *fakeRecoveryRepo) ClaimSttSummaryRedrive(_ context.Context, row model.SttRecoveryRow, attempt int, now time.Time) (string, error) {
	f.summaries = append(f.summaries, fmt.Sprintf("%s:%d", row.MeetingID, attempt))
	return now.Format(time.RFC3339Nano), nil
}
func (f *fakeRecoveryRepo) MarkSttFailureForClaim(_ context.Context, _, mid, code, claim string) error {
	f.marks = append(f.marks, mid+":"+code)
	return nil
}
func (f *fakeRecoveryRepo) MarkSttFailureObserved(_ context.Context, row model.SttRecoveryRow, code string) error {
	f.marks = append(f.marks, row.MeetingID+":"+code)
	return nil
}
func (f *fakeRecoveryRepo) ScanSttDeletionReferences(context.Context) (map[string]bool, error) {
	return f.refs, nil
}

type fakeMeetings struct{}

func (fakeMeetings) GetMeeting(_ context.Context, uid, mid string) (*model.Meeting, error) {
	return &model.Meeting{UserID: uid, MeetingID: mid, AudioKey: recKey(mid), Title: "기밀 제목", Notes: "private notes",
		Participants: []string{"a", "b", "c"}}, nil
}

type fakeDeleter struct {
	deleted []string
	blocked map[string]bool
	tried   int
}

func (f *fakeDeleter) DeleteFailedRecording(_ context.Context, _, mid string, mark model.SttFailureMark) (repository.SttFailedMeetingObjectsResult, error) {
	f.tried++
	if f.blocked[mid] {
		return repository.SttFailedMeetingObjectsResult{}, repository.ErrSttDeletionBlocked
	}
	f.deleted = append(f.deleted, mid+":"+mark.Code)
	return repository.SttFailedMeetingObjectsResult{Deleted: 2}, nil
}

type fakeS3 struct {
	transcripts map[string]time.Time
	extra       []s3types.Object
	copies      []*s3.CopyObjectInput
	endless     bool
}

func (f *fakeS3) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	prefix := aws.ToString(in.Prefix)
	mid := strings.Split(prefix, "/")[2]
	contents := []s3types.Object{{Key: aws.String(recKey(mid)), Size: aws.Int64(5_000_000),
		LastModified: aws.Time(recNow.Add(-5 * time.Hour))}}
	out := &s3.ListObjectsV2Output{Contents: append(contents, f.extra...)}
	if f.endless {
		out.IsTruncated, out.NextContinuationToken = aws.Bool(true), aws.String("next")
	}
	return out, nil
}
func (f *fakeS3) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	if modified, ok := f.transcripts[aws.ToString(in.Key)]; ok {
		return &s3.HeadObjectOutput{LastModified: aws.Time(modified), ETag: aws.String(`"etag"`), ContentType: aws.String("application/json")}, nil
	}
	return nil, &s3types.NotFound{}
}
func (f *fakeS3) CopyObject(_ context.Context, in *s3.CopyObjectInput, _ ...func(*s3.Options)) (*s3.CopyObjectOutput, error) {
	f.copies = append(f.copies, in)
	return &s3.CopyObjectOutput{}, nil
}

type fakeECS struct {
	busy    bool
	listErr error
	runErr  error
	runs    []*ecs.RunTaskInput
}

func (f *fakeECS) RunTask(_ context.Context, in *ecs.RunTaskInput, _ ...func(*ecs.Options)) (*ecs.RunTaskOutput, error) {
	f.runs = append(f.runs, in)
	if f.runErr != nil {
		return nil, f.runErr
	}
	return &ecs.RunTaskOutput{Tasks: []ecsTypes.Task{{TaskArn: aws.String("arn:task")}}}, nil
}
func (f *fakeECS) ListTasks(_ context.Context, in *ecs.ListTasksInput, _ ...func(*ecs.Options)) (*ecs.ListTasksOutput, error) {
	if in.DesiredStatus != ecsTypes.DesiredStatusRunning || aws.ToString(in.Cluster) != "ttobak-whisper" {
		return nil, errors.New("unexpected list")
	}
	if f.busy {
		return &ecs.ListTasksOutput{TaskArns: []string{"arn:busy"}}, nil
	}
	return &ecs.ListTasksOutput{}, f.listErr
}

type recoveryHarness struct {
	runner  *sttRecoveryRunner
	repo    *fakeRecoveryRepo
	s3      *fakeS3
	ecs     *fakeECS
	deleter *fakeDeleter
	logs    []string
	metrics []string
}

func newHarness(t *testing.T, mode string, rows ...model.SttRecoveryRow) *recoveryHarness {
	t.Helper()
	whisperCluster, whisperTaskDef, whisperContainer = "ttobak-whisper", "ttobak-whisper", "whisper"
	h := &recoveryHarness{repo: &fakeRecoveryRepo{rows: rows, refs: map[string]bool{}}, s3: &fakeS3{transcripts: map[string]time.Time{}},
		ecs: &fakeECS{}, deleter: &fakeDeleter{}}
	h.runner = &sttRecoveryRunner{
		repo: h.repo, meetings: fakeMeetings{}, deleter: h.deleter, storage: h.s3, ecs: h.ecs, bucket: "bucket",
		mode: mode, deleteMode: mode, whisperOK: true, now: func() time.Time { return recNow },
		inputs:  func(_ context.Context, m *model.Meeting, _ string) (string, int) { return "", len(m.Participants) },
		logf:    func(format string, args ...interface{}) { h.logs = append(h.logs, fmt.Sprintf(format, args...)) },
		emit:    func(line string) { h.metrics = append(h.metrics, line) },
		shuffle: func(int, func(i, j int)) {},
	}
	return h
}

func TestRecoveryStartsAtMostOneTaskPerTick(t *testing.T) {
	h := newHarness(t, sttRecoveryModeActive, stuckRow(recM1), stuckRow(recM2))
	if err := h.runner.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.ecs.runs) != 1 || len(h.repo.claims) != 1 || h.repo.claims[0] != recM1+":1" {
		t.Fatalf("runs=%d claims=%v", len(h.ecs.runs), h.repo.claims)
	}
	in := h.ecs.runs[0]
	claim := recNow.Format(time.RFC3339Nano)
	if aws.ToString(in.ClientToken) != sttRecoveryClientToken(recM1, recKey(recM1), claim) || aws.ToString(in.StartedBy) != "sttrec-"+recM1 {
		t.Fatalf("token=%q startedBy=%q", aws.ToString(in.ClientToken), aws.ToString(in.StartedBy))
	}
	if aws.ToString(in.TaskDefinition) != "ttobak-whisper" || len(in.Overrides.ContainerOverrides) != 1 {
		t.Fatal("recovery must use the production family and one container override")
	}
	override := in.Overrides.ContainerOverrides[0]
	if override.Command != nil || in.Overrides.ExecutionRoleArn != nil || in.Overrides.TaskRoleArn != nil {
		t.Fatal("recovery may only override environment variables")
	}
	env := map[string]string{}
	for _, kv := range override.Environment {
		env[aws.ToString(kv.Name)] = aws.ToString(kv.Value)
	}
	if env["AUDIO_KEY"] != recKey(recM1) || env["MEETING_ID"] != recM1 || env["USER_ID"] != recUser || env["NUM_SPEAKERS"] != "3" {
		t.Fatalf("env=%v", env)
	}
	if _, ok := env["OUTPUT_KEY"]; ok {
		t.Fatal("single-file recovery must write the canonical transcript key")
	}
	if _, ok := env["AUDIO_CROP"]; ok {
		t.Fatal("recovery is never a crop run")
	}
}

func TestRecoveryWaitsForABusyCluster(t *testing.T) {
	for _, tc := range []struct {
		name string
		ecs  *fakeECS
	}{{"busy", &fakeECS{busy: true}}, {"list error", &fakeECS{listErr: errors.New("denied")}}} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, sttRecoveryModeActive, stuckRow(recM1))
			h.runner.ecs = tc.ecs
			if err := h.runner.run(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(tc.ecs.runs) != 0 || len(h.repo.claims) != 0 {
				t.Fatal("a busy or unknown cluster must defer STT re-runs before any claim")
			}
		})
	}
}

func TestRecoveryReleasesAFailedRunTask(t *testing.T) {
	h := newHarness(t, sttRecoveryModeActive, stuckRow(recM1))
	h.ecs.runErr = errors.New("capacity")
	if err := h.runner.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.repo.marks) != 1 || h.repo.marks[0] != recM1+":"+model.SttFailureRunTaskFailed {
		t.Fatalf("marks=%v", h.repo.marks)
	}
}

func TestRecoveryLostClaimStartsNothing(t *testing.T) {
	h := newHarness(t, sttRecoveryModeActive, stuckRow(recM1))
	h.repo.claimErr = repository.ErrConditionFailed
	if err := h.runner.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.ecs.runs) != 0 {
		t.Fatal("a lost claim must not start a task")
	}
}

func TestRecoveryReportModeWritesNothing(t *testing.T) {
	stamp := recNow.Add(-8 * 24 * time.Hour).Format(time.RFC3339Nano)
	corrupt := stuckRow(recM2)
	corrupt.SttFailureCode, corrupt.SttFailedAt, corrupt.UpdatedAt = model.SttFailureAudioCorrupt, stamp, stamp
	transcribed := stuckRow("2b8f8a8e-1234-4abc-9def-0123456789ab")
	h := newHarness(t, sttRecoveryModeReport, stuckRow(recM1), corrupt, transcribed)
	h.s3.transcripts["transcripts/"+transcribed.MeetingID+".json"] = recNow.Add(-time.Hour)
	if err := h.runner.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.repo.claims)+len(h.repo.summaries)+len(h.repo.marks)+len(h.ecs.runs)+len(h.s3.copies)+len(h.deleter.deleted) != 0 {
		t.Fatalf("report mode wrote: %+v %+v", h.repo, h.deleter)
	}
	joined := strings.Join(h.logs, "\n")
	for _, want := range []string{"action=redrive_stt", "action=redrive_summary", "action=would_delete"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("report log lacks %s:\n%s", want, joined)
		}
	}
	for _, private := range []string{"기밀", "private notes", "비밀회의", recKey(recM1)} {
		if strings.Contains(joined, private) || strings.Contains(strings.Join(h.metrics, ""), private) {
			t.Fatalf("recovery log leaked %q", private)
		}
	}
	if len(h.metrics) != 1 || !strings.Contains(h.metrics[0], `"Ttobak/SttRecovery"`) {
		t.Fatalf("metrics=%v", h.metrics)
	}
}

func TestRecoveryRedrivesSummaryWithARealCopyEvent(t *testing.T) {
	h := newHarness(t, sttRecoveryModeActive, stuckRow(recM1))
	h.s3.transcripts["transcripts/"+recM1+".json"] = recNow.Add(-time.Hour)
	if err := h.runner.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.ecs.runs) != 0 || len(h.repo.summaries) != 1 || len(h.s3.copies) != 1 {
		t.Fatalf("runs=%d summaries=%v copies=%d", len(h.ecs.runs), h.repo.summaries, len(h.s3.copies))
	}
	copyIn := h.s3.copies[0]
	key := "transcripts/" + recM1 + ".json"
	if aws.ToString(copyIn.Key) != key || aws.ToString(copyIn.CopySource) != "bucket/"+key ||
		aws.ToString(copyIn.CopySourceIfMatch) != `"etag"` || copyIn.MetadataDirective != s3types.MetadataDirectiveReplace {
		t.Fatalf("copy=%+v", copyIn)
	}
}

// A user rediarization copies the audio to a fresh rediarize_ key without
// changing audioKey; while it runs, the old transcript must not be re-published.
func TestRecoveryNeverRepublishesTranscriptOlderThanARediarizeCopy(t *testing.T) {
	h := newHarness(t, sttRecoveryModeActive, stuckRow(recM1))
	h.s3.transcripts["transcripts/"+recM1+".json"] = recNow.Add(-time.Hour)
	h.s3.extra = []s3types.Object{{Key: aws.String("audio/" + recUser + "/" + recM1 + "/rediarize_x_recording.webm"),
		Size: aws.Int64(5_000_000), LastModified: aws.Time(recNow.Add(-30 * time.Minute))}}
	if err := h.runner.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.s3.copies) != 0 || len(h.repo.summaries) != 0 || len(h.ecs.runs) != 0 {
		t.Fatalf("copies=%d summaries=%v runs=%d", len(h.s3.copies), h.repo.summaries, len(h.ecs.runs))
	}
}

func TestRecoveryDeletePassHonoursReferencesAndBounds(t *testing.T) {
	stamp := recNow.Add(-8 * 24 * time.Hour).Format(time.RFC3339Nano)
	var rows []model.SttRecoveryRow
	for i := 0; i < 4; i++ {
		row := stuckRow(fmt.Sprintf("%d%s", i, recM1[1:]))
		row.SttFailureCode, row.SttFailedAt, row.UpdatedAt = model.SttFailureNoSpeech, stamp, stamp
		rows = append(rows, row)
	}
	h := newHarness(t, sttRecoveryModeActive, rows...)
	h.repo.refs[rows[0].MeetingID] = true
	if err := h.runner.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.deleter.deleted) != 2 || h.deleter.deleted[0] != rows[1].MeetingID+":NO_SPEECH" {
		t.Fatalf("deleted=%v", h.deleter.deleted)
	}
	if len(h.ecs.runs) != 0 {
		t.Fatal("terminal marks are never re-run")
	}
}

// Rows the guarded delete refuses must not consume the deletion budget.
func TestRecoveryDeletePassIsNotStarvedByRefusedRows(t *testing.T) {
	stamp := recNow.Add(-8 * 24 * time.Hour).Format(time.RFC3339Nano)
	var rows []model.SttRecoveryRow
	for i := 0; i < 5; i++ {
		row := stuckRow(fmt.Sprintf("%d%s", i, recM1[1:]))
		row.SttFailureCode, row.SttFailedAt, row.UpdatedAt = model.SttFailureNoSpeech, stamp, stamp
		rows = append(rows, row)
	}
	h := newHarness(t, sttRecoveryModeActive, rows...)
	h.deleter.blocked = map[string]bool{rows[0].MeetingID: true, rows[1].MeetingID: true}
	if err := h.runner.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.deleter.deleted) != 2 || h.deleter.deleted[0] != rows[2].MeetingID+":NO_SPEECH" {
		t.Fatalf("deleted=%v", h.deleter.deleted)
	}
	h = newHarness(t, sttRecoveryModeActive, rows...)
	h.deleter.blocked = map[string]bool{}
	for _, row := range rows {
		h.deleter.blocked[row.MeetingID] = true
	}
	h.runner.maxDeleteTries = 3
	if err := h.runner.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.deleter.tried != 3 {
		t.Fatalf("delete tries=%d, want the per-tick cap", h.deleter.tried)
	}
}

func TestRecoveryClaimBoundIdentifiers(t *testing.T) {
	token := sttRecoveryClientToken(recM1, recKey(recM1), "2026-10-17T12:00:00Z")
	if len(token) > 64 || strings.ContainsFunc(token, func(c rune) bool { return c < 33 || c > 126 }) {
		t.Fatalf("invalid ECS client token %q", token)
	}
	for _, other := range []string{
		sttRecoveryClientToken(recM1, recKey(recM1)+"x", "2026-10-17T12:00:00Z"),
		sttRecoveryClientToken(recM1, recKey(recM1), "2026-10-17T12:15:00Z"),
	} {
		if other == token {
			t.Fatal("replacement audio or a later claim must not reuse a client token")
		}
	}
	long := strings.Repeat("m", 128)
	for _, id := range []string{long, "meeting.1", "회의"} {
		if got := sttRecoveryStartedBy(id); len(got) > 128 || !ecsStartedByChars.MatchString(got) {
			t.Fatalf("invalid startedBy %q for %q", got, id)
		}
	}
}

func TestRecoveryDefersPartialAndOverBudgetWork(t *testing.T) {
	h := newHarness(t, sttRecoveryModeActive, stuckRow(recM1))
	h.s3.endless = true
	if err := h.runner.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.repo.claims) != 0 || !strings.Contains(h.metrics[0], `"facts_error":1`) {
		t.Fatalf("a truncated listing must defer the row: claims=%v metrics=%v", h.repo.claims, h.metrics)
	}
	h = newHarness(t, sttRecoveryModeActive, stuckRow(recM1), stuckRow(recM2))
	h.runner.rowBudget = time.Nanosecond
	ticks := 0
	h.runner.now = func() time.Time { ticks++; return recNow.Add(time.Duration(ticks) * time.Second) }
	if err := h.runner.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.repo.claims) != 0 || len(h.metrics) != 1 || !strings.Contains(h.metrics[0], `"rows_deferred":2`) {
		t.Fatalf("over-budget rows must be deferred with a tally: %v", h.metrics)
	}
	h = newHarness(t, sttRecoveryModeActive, stuckRow(recM1))
	h.runner.ecs = &fakeECS{listErr: errors.New("denied")}
	if err := h.runner.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.metrics[0], `"cluster_check_error":1`) {
		t.Fatalf("a failed cluster check must be distinguishable: %v", h.metrics)
	}
}

func TestRecoveryOffModeDoesNotScan(t *testing.T) {
	h := newHarness(t, sttRecoveryModeOff, stuckRow(recM1))
	h.repo.rows = nil
	h.runner.repo = nil // any scan would panic
	if err := h.runner.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if recoveryMode("ACTIVE") != sttRecoveryModeOff || recoveryMode("") != sttRecoveryModeOff || recoveryMode("active") != sttRecoveryModeActive {
		t.Fatal("unknown modes must fail closed to off")
	}
}

func TestWhisperInputsParity(t *testing.T) {
	original := loadDictionaryTerms
	t.Cleanup(func() { loadDictionaryTerms = original })
	loadDictionaryTerms = func(context.Context, string) ([]string, error) { return []string{"쿠버네티스", "EKS"}, nil }
	prompt, speakers := whisperInputs(context.Background(), &model.Meeting{Participants: []string{"a", "b"}, DiarizationSpeakerHint: 5}, "u")
	if prompt != "쿠버네티스, EKS" || speakers != 5 {
		t.Fatalf("prompt=%q speakers=%d", prompt, speakers)
	}
	if _, speakers := whisperInputs(context.Background(), &model.Meeting{Participants: []string{"a", "b"}}, "u"); speakers != 2 {
		t.Fatal("participants bound max_speakers")
	}
	if prompt, speakers := whisperInputs(context.Background(), nil, ""); prompt != "" || speakers != 0 {
		t.Fatal("no user means no dictionary and no bound")
	}
}

func TestCropRunTaskTokenUnchanged(t *testing.T) {
	whisperCluster, whisperTaskDef, whisperContainer = "ttobak-whisper", "ttobak-whisper", "whisper"
	in := whisperRunTaskInput("m", "u", "", "", "", 2, whisperTaskOptions{Crop: true, ClientToken: "crop-m"})
	if aws.ToString(in.ClientToken) != "crop-m" || in.StartedBy != nil {
		t.Fatalf("crop token=%q", aws.ToString(in.ClientToken))
	}
	found := false
	for _, kv := range in.Overrides.ContainerOverrides[0].Environment {
		found = found || aws.ToString(kv.Name) == "AUDIO_CROP"
	}
	if !found {
		t.Fatal("crop runs keep AUDIO_CROP")
	}
	if plain := whisperRunTaskInput("m", "u", "k", "", "", 0, whisperTaskOptions{}); plain.ClientToken != nil || plain.StartedBy != nil {
		t.Fatal("ordinary S3-event runs keep no client token")
	}
}

func TestRecoveryEnvelopeDispatch(t *testing.T) {
	if sttRecoverySource != "ttobak.recovery" || sttRecoveryDetailType != "SttRecoveryTick" {
		t.Fatal("the schedule input in GatewayStack depends on these exact values")
	}
}
