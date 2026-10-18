package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"path"
	"regexp"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecsTypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"golang.org/x/text/unicode/norm"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

// The scheduled recovery tick (ADR-031 amendment). EventBridge sends a fixed,
// parameterless envelope; a forged tick can only start the same bounded sweep.
const (
	sttRecoverySource     = "ttobak.recovery"
	sttRecoveryDetailType = "SttRecoveryTick"

	sttRecoveryModeOff    = "off"
	sttRecoveryModeReport = "report"
	sttRecoveryModeActive = "active"

	sttRecoveryStartedByPrefix = "sttrec-"
	sttRecoveryMetricNamespace = "Ttobak/SttRecovery"
)

type recoveryRepo interface {
	ScanSttRecoveryRows(ctx context.Context) ([]model.SttRecoveryRow, error)
	ClaimSttRecovery(ctx context.Context, row model.SttRecoveryRow, key string, attempt int, now time.Time) (string, error)
	ClaimSttSummaryRedrive(ctx context.Context, row model.SttRecoveryRow, attempt int, now time.Time) (string, error)
	MarkSttFailureForClaim(ctx context.Context, ownerID, meetingID, code, claim string) error
	MarkSttFailureObserved(ctx context.Context, row model.SttRecoveryRow, code string) error
	ScanSttDeletionReferences(ctx context.Context) (map[string]bool, error)
}

type recoveryMeetings interface {
	GetMeeting(ctx context.Context, userID, meetingID string) (*model.Meeting, error)
}

type recoveryDeleter interface {
	DeleteFailedRecording(ctx context.Context, ownerID, meetingID string, mark model.SttFailureMark) (repository.SttFailedMeetingObjectsResult, error)
}

type recoveryS3 interface {
	ListObjectsV2(ctx context.Context, params *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	HeadObject(ctx context.Context, params *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	CopyObject(ctx context.Context, params *s3.CopyObjectInput, optFns ...func(*s3.Options)) (*s3.CopyObjectOutput, error)
}

type sttRecoveryRunner struct {
	repo        recoveryRepo
	meetings    recoveryMeetings
	deleter     recoveryDeleter
	storage     recoveryS3
	ecs         ecsAPI
	bucket      string
	mode        string
	deleteMode  string
	whisperOK   bool
	inputs      func(ctx context.Context, meeting *model.Meeting, userID string) (string, int)
	now         func() time.Time
	maxSTT      int
	maxSummary  int
	maxDeletes  int
	actBudget   time.Duration
	logf        func(format string, args ...interface{})
	emit        func(line string)
	maxListPage int
	// maxDeleteTries bounds delete attempts per tick, including refused ones;
	// shuffle randomizes their order so refused rows cannot starve others.
	maxDeleteTries int
	shuffle        func(n int, swap func(i, j int))
	// rowBudget stops per-row S3 work well inside the 5-minute Lambda timeout
	// so the tally is still emitted.
	rowBudget time.Duration
}

// errSttListingTruncated defers a row whose audio prefix exceeds the listing
// cap instead of classifying it from partial facts.
var errSttListingTruncated = errors.New("audio prefix listing exceeds the page cap")

func recoveryMode(raw string) string {
	switch raw {
	case sttRecoveryModeReport, sttRecoveryModeActive:
		return raw
	}
	return sttRecoveryModeOff
}

func runSttRecoveryTick(ctx context.Context) error {
	runner := &sttRecoveryRunner{
		repo:       repo,
		meetings:   repo.MetadataView(),
		deleter:    service.NewMeetingService(repo),
		storage:    s3Client,
		ecs:        ecsClient,
		bucket:     outputBucketName,
		mode:       recoveryMode(os.Getenv("STT_RECOVERY_MODE")),
		deleteMode: recoveryMode(os.Getenv("STT_RECOVERY_DELETE_MODE")),
		whisperOK:  whisperCluster != "" && whisperTaskDef != "" && whisperContainer == "whisper",
		inputs:     whisperInputs,
	}
	return runner.run(ctx)
}

func (r *sttRecoveryRunner) defaults() {
	if r.now == nil {
		r.now = time.Now
	}
	if r.maxSTT == 0 {
		r.maxSTT = 1
	}
	if r.maxSummary == 0 {
		r.maxSummary = 2
	}
	if r.maxDeletes == 0 {
		r.maxDeletes = 2
	}
	if r.actBudget == 0 {
		r.actBudget = 3 * time.Minute
	}
	if r.logf == nil {
		r.logf = log.Printf
	}
	if r.emit == nil {
		r.emit = func(line string) { fmt.Println(line) }
	}
	if r.maxListPage == 0 {
		r.maxListPage = 10
	}
	if r.maxDeleteTries == 0 {
		r.maxDeleteTries = 10
	}
	if r.shuffle == nil {
		r.shuffle = rand.Shuffle
	}
	if r.rowBudget == 0 {
		r.rowBudget = 4 * time.Minute
	}
}

type sttRecoveryTally map[string]int

// run returns nil except on fatal setup errors: a lost tick is harmless and a
// retried tick is bounded by the same conditional claims.
func (r *sttRecoveryRunner) run(ctx context.Context) error {
	r.defaults()
	if r.mode == sttRecoveryModeOff && r.deleteMode == sttRecoveryModeOff {
		return nil
	}
	started := r.now()
	rows, err := r.repo.ScanSttRecoveryRows(ctx)
	if err != nil {
		return fmt.Errorf("stt recovery scan: %w", err)
	}
	tally := sttRecoveryTally{}
	var deletions []model.SttRecoveryRow
	sttDone, summaryDone := 0, 0
	clusterChecked, clusterBusy := false, false
	for i, row := range rows {
		now := r.now()
		if now.Sub(started) > r.rowBudget {
			tally["rows_deferred"] += len(rows) - i
			break
		}
		facts := service.SttRecoveryFacts{}
		if needsSttFacts(row) {
			facts, err = r.facts(ctx, row)
			if err != nil {
				tally["facts_error"]++
				r.logf("stt-recovery meeting=%s status=%s action=facts_error", row.MeetingID, row.Status)
				continue
			}
		}
		decision := service.ClassifySttRecovery(row, facts, now)
		tally[string(decision.Action)]++
		if decision.Action == service.SttRecoverySkip {
			continue
		}
		r.logf("stt-recovery meeting=%s status=%s action=%s reason=%s bytes=%d attempt=%d mode=%s",
			row.MeetingID, row.Status, decision.Action, decision.Reason, facts.AudioSize, decision.Attempt, r.mode)
		if decision.Action == service.SttRecoveryTerminal {
			if service.SttFailureDeletionDue(row, now) {
				deletions = append(deletions, row)
			}
			continue
		}
		if r.mode != sttRecoveryModeActive || now.Sub(started) > r.actBudget {
			continue
		}
		switch decision.Action {
		case service.SttRecoveryRedriveSTT:
			if sttDone >= r.maxSTT {
				tally["stt_deferred"]++
				continue
			}
			if !r.whisperOK {
				tally["whisper_unconfigured"]++
				continue
			}
			if !clusterChecked {
				var checkErr error
				clusterBusy, checkErr = r.clusterBusy(ctx)
				clusterChecked, clusterBusy = true, clusterBusy || checkErr != nil
				if checkErr != nil {
					tally["cluster_check_error"]++
				}
			}
			if clusterBusy {
				tally["cluster_busy"]++
				continue
			}
			sttDone++
			tally[r.redriveSTT(ctx, row, decision, now)]++
		case service.SttRecoveryRedriveSummary:
			if summaryDone >= r.maxSummary {
				tally["summary_deferred"]++
				continue
			}
			summaryDone++
			tally[r.redriveSummary(ctx, row, decision, facts, now)]++
		case service.SttRecoveryMarkExhausted:
			err := r.repo.MarkSttFailureObserved(ctx, row, model.SttFailureRecoveryExhausted)
			tally[outcome("exhausted", err)]++
		}
	}
	r.deletePass(ctx, deletions, started, tally)
	r.emitTally(tally, len(rows))
	return nil
}

func outcome(prefix string, err error) string {
	switch {
	case err == nil:
		return prefix + "_ok"
	case errors.Is(err, repository.ErrConditionFailed):
		return prefix + "_superseded"
	}
	return prefix + "_error"
}

// needsSttFacts avoids S3 calls for rows the predicate rejects on state alone.
func needsSttFacts(row model.SttRecoveryRow) bool {
	switch row.Status {
	case model.StatusTranscribing, model.StatusError, model.StatusRecording:
	default:
		return false
	}
	if row.AudioCrop != nil || row.SummarizeRetryClaimedAt != "" || row.SummaryRetryPending || row.SummaryConflictCode != "" {
		return false
	}
	if model.IsTerminalSttFailure(row.SttFailureCode) && row.Status != model.StatusTranscribing {
		return false
	}
	return service.SafeSttIdentifier(row.UserID) && service.SafeSttIdentifier(row.MeetingID)
}

func isS3NotFound(err error) bool {
	var notFound *s3types.NotFound
	var noKey *s3types.NoSuchKey
	if errors.As(err, &notFound) || errors.As(err, &noKey) {
		return true
	}
	var status interface{ HTTPStatusCode() int }
	return errors.As(err, &status) && status.HTTPStatusCode() == 404
}

func (r *sttRecoveryRunner) facts(ctx context.Context, row model.SttRecoveryRow) (service.SttRecoveryFacts, error) {
	var facts service.SttRecoveryFacts
	prefix := "audio/" + row.UserID + "/" + row.MeetingID + "/"
	var token *string
	for page := 0; page < r.maxListPage; page++ {
		out, err := r.storage.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(r.bucket), Prefix: aws.String(prefix), ContinuationToken: token})
		if err != nil {
			return facts, err
		}
		for _, object := range out.Contents {
			key := aws.ToString(object.Key)
			if modified := aws.ToTime(object.LastModified); modified.After(facts.NewestAudioModified) {
				facts.NewestAudioModified = modified
			}
			if row.AudioKey != "" && (key == row.AudioKey || norm.NFC.String(key) == row.AudioKey) {
				facts.AudioFound, facts.AudioSize, facts.AudioModified = true, aws.ToInt64(object.Size), aws.ToTime(object.LastModified)
			}
			if service.IsSttCheckpointObjectName(path.Base(key)) {
				facts.CheckpointFound = true
			}
		}
		if !aws.ToBool(out.IsTruncated) || out.NextContinuationToken == nil {
			token = nil
			break
		}
		token = out.NextContinuationToken
	}
	if token != nil {
		return facts, errSttListingTruncated
	}
	head, err := r.storage.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(r.bucket), Key: aws.String("transcripts/" + row.MeetingID + ".json")})
	if err != nil {
		if isS3NotFound(err) {
			return facts, nil
		}
		return facts, err
	}
	facts.TranscriptFound, facts.TranscriptModified = true, aws.ToTime(head.LastModified)
	facts.TranscriptETag, facts.TranscriptType = aws.ToString(head.ETag), aws.ToString(head.ContentType)
	return facts, nil
}

// clusterBusy is the single-flight rule: any task the Whisper cluster wants
// running (production, benchmark or still provisioning) defers STT re-runs.
// The caller treats a check error as busy so recovery never fans out GPU hosts.
func (r *sttRecoveryRunner) clusterBusy(ctx context.Context) (bool, error) {
	out, err := r.ecs.ListTasks(ctx, &ecs.ListTasksInput{
		Cluster: aws.String(whisperCluster), DesiredStatus: ecsTypes.DesiredStatusRunning, MaxResults: aws.Int32(1),
	})
	if err != nil {
		r.logf("stt-recovery cluster check failed; deferring STT re-runs")
		return true, err
	}
	return len(out.TaskArns) > 0, nil
}

var ecsStartedByChars = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// sttRecoveryStartedBy is a valid ECS startedBy (at most 128 of [A-Za-z0-9_-/])
// for any meeting ID; UUID IDs stay readable for list-tasks filtering.
func sttRecoveryStartedBy(meetingID string) string {
	if len(sttRecoveryStartedByPrefix)+len(meetingID) <= 128 && ecsStartedByChars.MatchString(meetingID) {
		return sttRecoveryStartedByPrefix + meetingID
	}
	sum := sha256.Sum256([]byte(meetingID))
	return sttRecoveryStartedByPrefix + hex.EncodeToString(sum[:16])
}

// sttRecoveryClientToken binds RunTask idempotency to this exact claim: the
// meeting, the claimed audio key and the unique claim value. SDK retries of
// one call reuse it; a later claim (or replacement audio) never collides with
// an earlier task. 7 + 56 = 63 characters, inside ECS's 64-character limit.
func sttRecoveryClientToken(meetingID, key, claim string) string {
	sum := sha256.Sum256([]byte(meetingID + "\x00" + key + "\x00" + claim))
	return sttRecoveryStartedByPrefix + hex.EncodeToString(sum[:])[:56]
}

func (r *sttRecoveryRunner) redriveSTT(ctx context.Context, row model.SttRecoveryRow, decision service.SttRecoveryDecision, now time.Time) string {
	meeting, err := r.meetings.GetMeeting(ctx, row.UserID, row.MeetingID)
	if err != nil {
		return "stt_read_error"
	}
	if meeting == nil || meeting.AudioKey != decision.Key {
		return "stt_superseded"
	}
	claim, err := r.repo.ClaimSttRecovery(ctx, row, decision.Key, decision.Attempt, now)
	if err != nil {
		return outcome("stt_claim", err)
	}
	prompt, speakers := r.inputs(ctx, meeting, row.UserID)
	input := whisperRunTaskInput(row.MeetingID, row.UserID, decision.Key, prompt, "", speakers, whisperTaskOptions{
		ClientToken: sttRecoveryClientToken(row.MeetingID, decision.Key, claim),
		StartedBy:   sttRecoveryStartedBy(row.MeetingID),
	})
	out, err := r.ecs.RunTask(ctx, input)
	if err == nil && len(out.Failures) == 0 && len(out.Tasks) > 0 {
		r.logf("stt-recovery meeting=%s action=stt_started attempt=%d", row.MeetingID, decision.Attempt)
		return "stt_started"
	}
	r.logf("stt-recovery meeting=%s action=runtask_failed attempt=%d", row.MeetingID, decision.Attempt)
	release, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if markErr := r.repo.MarkSttFailureForClaim(release, row.UserID, row.MeetingID, model.SttFailureRunTaskFailed, claim); markErr != nil {
		return "stt_release_error"
	}
	return "stt_runtask_failed"
}

// redriveSummary re-emits a real S3 Object Created event for the existing
// transcript by copying it onto itself, pinned to the observed ETag.
func (r *sttRecoveryRunner) redriveSummary(ctx context.Context, row model.SttRecoveryRow, decision service.SttRecoveryDecision, facts service.SttRecoveryFacts, now time.Time) string {
	if facts.TranscriptETag == "" {
		return "summary_no_etag"
	}
	claim, err := r.repo.ClaimSttSummaryRedrive(ctx, row, decision.Attempt, now)
	if err != nil {
		return outcome("summary_claim", err)
	}
	key := "transcripts/" + row.MeetingID + ".json"
	contentType := facts.TranscriptType
	if contentType == "" {
		contentType = "application/json"
	}
	_, err = r.storage.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket: aws.String(r.bucket), Key: aws.String(key), CopySource: aws.String(r.bucket + "/" + key),
		CopySourceIfMatch: aws.String(facts.TranscriptETag), MetadataDirective: s3types.MetadataDirectiveReplace,
		ContentType: aws.String(contentType), Metadata: map[string]string{"ttobak-redrive": strconv.Itoa(decision.Attempt)},
	})
	if err == nil {
		r.logf("stt-recovery meeting=%s action=summary_redriven attempt=%d", row.MeetingID, decision.Attempt)
		return "summary_redriven"
	}
	release, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if markErr := r.repo.MarkSttFailureForClaim(release, row.UserID, row.MeetingID, model.SttFailureRedriveFailed, claim); markErr != nil {
		return "summary_release_error"
	}
	return "summary_redrive_failed"
}

func (r *sttRecoveryRunner) deletePass(ctx context.Context, due []model.SttRecoveryRow, started time.Time, tally sttRecoveryTally) {
	if r.deleteMode == sttRecoveryModeOff || len(due) == 0 {
		return
	}
	refs, err := r.repo.ScanSttDeletionReferences(ctx)
	if err != nil {
		tally["delete_reference_error"]++
		r.logf("stt-recovery action=delete_reference_error")
		return
	}
	deleted, tries := 0, 0
	r.shuffle(len(due), func(i, j int) { due[i], due[j] = due[j], due[i] })
	for _, row := range due {
		if refs[row.MeetingID] {
			tally["delete_blocked"]++
			r.logf("stt-recovery meeting=%s action=delete_blocked reason=referenced", row.MeetingID)
			continue
		}
		if r.deleteMode != sttRecoveryModeActive {
			tally["would_delete"]++
			r.logf("stt-recovery meeting=%s action=would_delete code=%s", row.MeetingID, row.SttFailureCode)
			continue
		}
		if deleted >= r.maxDeletes || tries >= r.maxDeleteTries || r.now().Sub(started) > r.actBudget {
			tally["delete_deferred"]++
			continue
		}
		// Only committed deletions consume the deletion budget; a refused or
		// superseded candidate consumes a try, never a deletion.
		tries++
		result, err := r.deleter.DeleteFailedRecording(ctx, row.UserID, row.MeetingID, model.SttFailureMark{Code: row.SttFailureCode, FailedAt: row.SttFailedAt})
		switch {
		case result.Failed > 0 || errors.Is(err, repository.ErrSttDeletionPostCommit):
			deleted++
			tally["deleted"]++
			tally["SttDeleteS3CleanupFailed"] += max(result.Failed, 1)
		case err == nil:
			deleted++
			tally["deleted"]++
		case errors.Is(err, repository.ErrConditionFailed):
			tally["delete_superseded"]++
		case errors.Is(err, repository.ErrSttDeletionBlocked):
			tally["delete_blocked"]++
		default:
			// The repository resolves lost responses by reading the row back;
			// an error left here may still hide a commit, so it conservatively
			// consumes a deletion slot to keep the per-tick bound.
			deleted++
			tally["delete_error"]++
		}
		r.logf("stt-recovery meeting=%s action=delete objects=%d failed=%d outcome=%s", row.MeetingID, result.Deleted, result.Failed, deleteOutcome(err))
	}
}

func deleteOutcome(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, repository.ErrConditionFailed):
		return "superseded"
	case errors.Is(err, repository.ErrSttDeletionBlocked):
		return "blocked"
	}
	return "error"
}

// emitTally writes one CloudWatch embedded-metric-format line per tick with
// counts only: no IDs, keys or meeting content.
func (r *sttRecoveryRunner) emitTally(tally sttRecoveryTally, scanned int) {
	tally["scanned"] = scanned
	metrics := make([]map[string]string, 0, len(tally))
	doc := map[string]interface{}{}
	for name, value := range tally {
		metrics = append(metrics, map[string]string{"Name": name, "Unit": "Count"})
		doc[name] = value
	}
	doc["_aws"] = map[string]interface{}{
		"Timestamp": r.now().UnixMilli(),
		"CloudWatchMetrics": []map[string]interface{}{{
			"Namespace": sttRecoveryMetricNamespace, "Dimensions": [][]string{{}}, "Metrics": metrics,
		}},
	}
	body, err := json.Marshal(doc)
	if err == nil {
		r.emit(string(body))
	}
}
