package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type resummaryGenerateFunc func(context.Context, *model.Meeting, []model.Attachment) (string, error)

func (f resummaryGenerateFunc) GenerateResummary(ctx context.Context, m *model.Meeting, a []model.Attachment) (string, error) {
	return f(ctx, m, a)
}

type resummaryTestRepo struct {
	*mockMeetingRepo
	state                                  *model.ResummaryState
	captures, queues, starts, completes    int
	reads                                  []string
	revoked                                bool
	bindingError, completeError, failError error
}

func (r *resummaryTestRepo) CaptureResummary(_ context.Context, owner, mid, requester string) (*model.SummarySnapshot, error) {
	r.captures++
	if r.revoked {
		return nil, repository.ErrSummaryAccess
	}
	m := r.meetings[meetingKey(owner, mid)]
	if m == nil {
		return nil, nil
	}
	copy := *m
	fields := map[string]model.SummaryValue{}
	stored := map[string]interface{}{}
	for k, v := range map[string]interface{}{"content": m.Content, "notes": m.Notes, "transcriptA": m.TranscriptA, "transcriptB": m.TranscriptB, "selectedTranscript": m.SelectedTranscript, "transcriptSegments": m.TranscriptSegments, "status": m.Status, "noteLanguage": m.NoteLanguage} {
		fields[k] = model.SummaryValue{Present: true, Value: v}
		stored[k] = v
	}
	return &model.SummarySnapshot{Meeting: &copy, Checks: []model.SummaryCheck{{PK: "USER#" + owner, SK: "MEETING#" + mid, Exists: true, Fields: fields}}, Stored: stored, TextStates: map[string]*model.AttachmentTextState{}}, nil
}
func (r *resummaryTestRepo) GetResummary(context.Context, string) (*model.ResummaryState, error) {
	if r.state == nil {
		return nil, nil
	}
	copy := *r.state
	return &copy, nil
}
func (r *resummaryTestRepo) QueueResummary(_ context.Context, _ *model.SummarySnapshot, prior, next *model.ResummaryState) error {
	if !reflect.DeepEqual(prior, r.state) {
		return repository.ErrConditionFailed
	}
	r.queues++
	copy := *next
	r.state = &copy
	return nil
}
func (r *resummaryTestRepo) StartResummary(_ context.Context, _ *model.SummarySnapshot, state *model.ResummaryState, now time.Time, lease int64) error {
	if r.state == nil || r.state.RunID != state.RunID || r.state.Status != model.AnalysisQueued {
		return repository.ErrConditionFailed
	}
	r.starts++
	r.state.Status = model.AnalysisRunning
	r.state.LeaseUntil = lease
	return nil
}
func (r *resummaryTestRepo) CompleteResummary(_ context.Context, snapshot *model.SummarySnapshot, state *model.ResummaryState, content, coverage, hash string, now time.Time) error {
	if r.completeError != nil {
		return r.completeError
	}
	if r.state.RunID != state.RunID || r.state.Status != model.AnalysisRunning || r.state.LeaseUntil != state.LeaseUntil {
		return repository.ErrConditionFailed
	}
	r.completes++
	m := r.meetings[meetingKey(state.OwnerID, snapshot.Meeting.MeetingID)]
	m.Content = content
	m.AttachmentSummarySources = coverage
	r.state.Status = model.AnalysisSucceeded
	r.state.ResultHash = hash
	r.state.LeaseUntil = 0
	return nil
}
func (r *resummaryTestRepo) FailResummary(_ context.Context, _ string, state *model.ResummaryState, code string, now time.Time) error {
	if r.failError != nil {
		return r.failError
	}
	if r.state == nil || r.state.RunID != state.RunID || r.state.Status != state.Status || r.state.LeaseUntil != state.LeaseUntil {
		return repository.ErrConditionFailed
	}
	r.state.Status = model.AnalysisFailed
	r.state.ErrorCode = code
	r.state.LeaseUntil = 0
	return nil
}
func (r *resummaryTestRepo) ReadResummaryTranscript(_ context.Context, _, field, value string) (string, *model.SummaryObject, error) {
	r.reads = append(r.reads, field)
	if strings.HasPrefix(value, "s3://") {
		return "resolved " + field, &model.SummaryObject{Bucket: "bucket", Key: "transcripts/meeting/" + field + ".txt", ETag: "etag"}, nil
	}
	return value, nil, nil
}
func (r *resummaryTestRepo) CheckResummaryObjects(context.Context, string, []model.SummaryObject) error {
	return r.bindingError
}
func newResummaryFixture(t *testing.T) (*ResummaryService, *resummaryTestRepo, *int) {
	t.Helper()
	repo := &resummaryTestRepo{mockMeetingRepo: newMockMeetingRepo()}
	repo.addMeeting(&model.Meeting{UserID: "owner", MeetingID: "meeting", Status: model.StatusDone, Content: "old summary", Notes: "saved memo", TranscriptA: "source A", TranscriptB: "s3://bucket/transcripts/meeting/transcriptB.txt", SelectedTranscript: "B"})
	calls := 0
	generator := resummaryGenerateFunc(func(_ context.Context, m *model.Meeting, _ []model.Attachment) (string, error) {
		calls++
		if m.TranscriptA != "" || m.TranscriptB != "resolved transcriptB" || m.Notes != "saved memo" {
			t.Fatalf("source changed or unselected text leaked: %+v", m)
		}
		return "새 요약", nil
	})
	service := NewResummaryService(repo, &MeetingService{repo: repo}, generator, nil, func(context.Context, model.SummaryRequested) error { return nil })
	service.now = func() time.Time { return time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC) }
	return service, repo, &calls
}

func TestSharingSafeResummaryExcludesOldDerivationAndRetainsConcurrentNotes(t *testing.T) {
	t.Setenv("SUMMARY_PRIVACY_WRITER_ENABLED", "1")
	s, repo, _ := newResummaryFixture(t)
	m := repo.meetings[meetingKey("owner", "meeting")]
	m.LinkedMeetingIDs = []string{"PRIVATE_PREDECESSOR"}
	m.ActionItems, m.Insights, m.LiveSummary, m.Sentiment = "PRIVATE_TASK", "PRIVATE_INSIGHT", "PRIVATE_DRAFT", "PRIVATE_SENTIMENT"
	m.Tags = []string{"PRIVATE_TAG"}
	s.generator = resummaryGenerateFunc(func(_ context.Context, snapshot *model.Meeting, _ []model.Attachment) (string, error) {
		if snapshot.Content != "" || snapshot.ActionItems != "" || snapshot.Insights != "" || snapshot.LiveSummary != "" ||
			snapshot.Sentiment != "" || len(snapshot.Tags) != 0 || len(snapshot.LinkedMeetingIDs) != 0 {
			t.Fatal("sharing-safe model received previous private derivation")
		}
		if snapshot.Notes != "saved memo" || snapshot.TranscriptB != "resolved transcriptB" {
			t.Fatal("sharing-safe model lost current own notes/transcript")
		}
		m.Notes = "human note written during generation"
		return "late safe generation", nil
	})
	status, err := s.RequestSharingSafe(context.Background(), "owner", "meeting")
	if err != nil || !status.SharingSafe {
		t.Fatalf("explicit owner mode did not survive request: %+v %v", status, err)
	}
	if err := s.Process(context.Background(), model.SummaryRequested{MeetingID: "meeting", RunID: status.RunID}); err != nil {
		t.Fatal(err)
	}
	if repo.completes != 0 || m.Content != "old summary" || m.Notes != "human note written during generation" ||
		repo.state.Status != model.AnalysisFailed || repo.state.ErrorCode != model.AnalysisSourceChanged {
		t.Fatalf("safe regeneration overwrote concurrent human source: completes=%d state=%+v", repo.completes, repo.state)
	}
}

func TestSharingSafePreparationAllowsOnlyVerifiedDocuments(t *testing.T) {
	s, _, _ := newResummaryFixture(t)
	snapshot := &model.SummarySnapshot{Meeting: &model.Meeting{UserID: "owner", MeetingID: "meeting",
		Status: model.StatusDone, Notes: "own note", Content: "PRIVATE_GENERATED"},
		SharingSafe: true, TextStates: map[string]*model.AttachmentTextState{},
		Attachments: []model.Attachment{
			{AttachmentID: "image", Type: model.AttachTypePhoto, ProcessedContent: "PRIVATE_IMAGE", Description: "PRIVATE_DESCRIPTION"},
			{AttachmentID: "document", Type: model.AttachTypeDocument, ProcessedContent: "PRIVATE_PREVIEW", Description: "PRIVATE_DESCRIPTION",
				OriginalKey: "files/owner/meeting/unready.pdf"},
		}}
	m, attachments, _, err := s.prepare(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if m.Content != "" || len(attachments) != 1 || !attachments[0].SummaryOmitted ||
		attachments[0].ProcessedContent != "" || attachments[0].Description != "" || attachments[0].ExtractedText != nil {
		t.Fatalf("legacy attachment derivation entered safe evidence: %+v", attachments)
	}
}
func TestResummaryOwnerEditAuthorizationAndDuplicateRequest(t *testing.T) {
	s, r, _ := newResummaryFixture(t)
	r.shares[shareKey("reader", "meeting")] = &model.Share{MeetingID: "meeting", OwnerID: "owner", Permission: model.PermissionRead}
	r.shares[shareKey("editor", "meeting")] = &model.Share{MeetingID: "meeting", OwnerID: "owner", Permission: model.PermissionEdit}
	if _, err := s.Request(context.Background(), "reader", "meeting"); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if r.queues != 0 || len(r.reads) != 0 {
		t.Fatal("reader started work")
	}
	state, err := s.Request(context.Background(), "editor", "meeting")
	if err != nil || state.Status != model.AnalysisQueued {
		t.Fatalf("%+v %v", state, err)
	}
	second, err := s.Request(context.Background(), "owner", "meeting")
	if err != nil || second.RunID != state.RunID || r.queues != 1 {
		t.Fatal("duplicate run")
	}
	if r.state.RequestedBy != "editor" {
		t.Fatal("requester not retained for worker revalidation")
	}
}
func TestResummarySelectedSnapshotAndAtomicCompletion(t *testing.T) {
	s, r, calls := newResummaryFixture(t)
	result, err := s.Request(context.Background(), "owner", "meeting")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.reads) != 0 {
		t.Fatal("queue hydrated S3")
	}
	var enqueued []string
	s.SetTermsEnqueuer(func(_ context.Context, owner, meeting string) { enqueued = append(enqueued, owner+"/"+meeting) })
	event := model.SummaryRequested{MeetingID: "meeting", RunID: result.RunID}
	if err := s.Process(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(enqueued) != 1 || enqueued[0] != "owner/meeting" {
		t.Fatalf("terms extraction must follow CompleteResummary once: %v", enqueued)
	}
	if r.completes != 1 || *calls != 1 || r.state.Status != model.AnalysisSucceeded || r.meetings[meetingKey("owner", "meeting")].Content != "새 요약" {
		t.Fatalf("run %+v calls=%d", r.state, *calls)
	}
	if !reflect.DeepEqual(r.reads, []string{"transcriptB"}) {
		t.Fatalf("unexpected hydration: %v", r.reads)
	}
	if err := s.Process(context.Background(), event); err != nil || *calls != 1 {
		t.Fatal("duplicate event generated again")
	}
}
func TestResummarySourceChangesAndRevocationPreserveHumanContent(t *testing.T) {
	for _, kind := range []string{"content", "notes", "transcript", "noteLanguage", "revoked", "deleted"} {
		t.Run(kind, func(t *testing.T) {
			s, r, _ := newResummaryFixture(t)
			requested, err := s.Request(context.Background(), "owner", "meeting")
			if err != nil {
				t.Fatal(err)
			}
			s.generator = resummaryGenerateFunc(func(context.Context, *model.Meeting, []model.Attachment) (string, error) {
				m := r.meetings[meetingKey("owner", "meeting")]
				switch kind {
				case "content":
					m.Content = "human save"
				case "notes":
					m.Notes = "new notes"
				case "transcript":
					m.TranscriptB = "changed selected text"
				case "noteLanguage":
					m.NoteLanguage = model.NoteLanguageEnglish
				case "revoked":
					r.revoked = true
				case "deleted":
					delete(r.meetings, meetingKey("owner", "meeting"))
				}
				return "stale generated text", nil
			})
			if err := s.Process(context.Background(), model.SummaryRequested{MeetingID: "meeting", RunID: requested.RunID}); err != nil {
				t.Fatal(err)
			}
			if r.completes != 0 || r.state.Status != model.AnalysisFailed {
				t.Fatalf("%+v", r.state)
			}
			if m := r.meetings[meetingKey("owner", "meeting")]; m != nil && m.Content == "stale generated text" {
				t.Fatal("human content overwritten")
			}
			if kind == "noteLanguage" && r.state.ErrorCode != model.AnalysisSourceChanged {
				t.Fatal(r.state.ErrorCode)
			}
			if kind == "revoked" && r.state.ErrorCode != "ACCESS_REVOKED" {
				t.Fatal(r.state.ErrorCode)
			}
		})
	}
}
func TestResummaryPublishLeaseAndProviderFailures(t *testing.T) {
	t.Run("publish", func(t *testing.T) {
		s, r, _ := newResummaryFixture(t)
		s.publish = func(context.Context, model.SummaryRequested) error { return errors.New("transport") }
		if _, err := s.Request(context.Background(), "owner", "meeting"); !errors.Is(err, ErrResummaryPublish) {
			t.Fatal(err)
		}
		if r.state.Status != model.AnalysisFailed || r.state.ErrorCode != model.AnalysisPublishFailed || r.meetings[meetingKey("owner", "meeting")].Content != "old summary" {
			t.Fatal(r.state)
		}
	})
	for _, kind := range []string{"expired", "provider", "blank", "object", "write", "old event"} {
		t.Run(kind, func(t *testing.T) {
			s, r, calls := newResummaryFixture(t)
			requested, err := s.Request(context.Background(), "owner", "meeting")
			if err != nil {
				t.Fatal(err)
			}
			event := model.SummaryRequested{MeetingID: "meeting", RunID: requested.RunID}
			switch kind {
			case "expired":
				r.state.LeaseUntil = s.now().Add(-time.Second).UnixMilli()
			case "provider":
				s.generator = resummaryGenerateFunc(func(context.Context, *model.Meeting, []model.Attachment) (string, error) {
					return "", errors.New("private provider text")
				})
			case "blank":
				s.generator = resummaryGenerateFunc(func(context.Context, *model.Meeting, []model.Attachment) (string, error) { return " ", nil })
			case "object":
				r.bindingError = repository.ErrConditionFailed
			case "write":
				r.completeError = errors.New("write failed")
			case "old event":
				event.RunID = "obsolete"
			}
			err = s.Process(context.Background(), event)
			if kind == "write" {
				if !errors.Is(err, r.completeError) {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if r.meetings[meetingKey("owner", "meeting")].Content != "old summary" || r.completes != 0 {
				t.Fatal("failure changed summary")
			}
			if kind == "old event" {
				if r.state.Status != model.AnalysisQueued || *calls != 0 {
					t.Fatal("old event started work")
				}
			} else if r.state.Status != model.AnalysisFailed {
				t.Fatal(r.state)
			}
		})
	}
}
func TestResummaryStatusExpiryAndBusySource(t *testing.T) {
	s, r, _ := newResummaryFixture(t)
	status, err := s.Get(context.Background(), "owner", "meeting")
	if err != nil || status.Status != model.AnalysisUnknown {
		t.Fatal(status, err)
	}
	r.meetings[meetingKey("owner", "meeting")].Status = model.StatusTranscribing
	if _, err := s.Request(context.Background(), "owner", "meeting"); !errors.Is(err, ErrResummaryBusy) {
		t.Fatal(err)
	}
	r.meetings[meetingKey("owner", "meeting")].Status = model.StatusDone
	if _, err := s.Request(context.Background(), "owner", "meeting"); err != nil {
		t.Fatal(err)
	}
	r.state.LeaseUntil = 0
	status, err = s.Get(context.Background(), "owner", "meeting")
	if err != nil || status.Status != "failed" || status.ErrorCode != "INTERRUPTED" {
		t.Fatal(status, err)
	}
}

func TestResummaryCompletionFailuresRemainObservable(t *testing.T) {
	cleanupErr := errors.New("spill cleanup failed")
	stateErr := errors.New("failure state write failed")
	for _, completion := range []error{errors.New("commit failed"), errors.Join(repository.ErrConditionFailed, cleanupErr)} {
		for _, failState := range []bool{false, true} {
			s, r, calls := newResummaryFixture(t)
			requested, err := s.Request(context.Background(), "owner", "meeting")
			if err != nil {
				t.Fatal(err)
			}
			r.completeError = completion
			s.SetTermsEnqueuer(func(context.Context, string, string) { t.Fatal("a failed publication must not enqueue terms") })
			if failState {
				r.failError = stateErr
			}
			event := model.SummaryRequested{MeetingID: "meeting", RunID: requested.RunID}
			err = s.Process(context.Background(), event)
			if !errors.Is(err, completion) || failState && !errors.Is(err, stateErr) {
				t.Fatalf("persistence/cleanup failure swallowed: %v", err)
			}
			if r.meetings[meetingKey("owner", "meeting")].Content != "old summary" || *calls != 1 {
				t.Fatal("failed publication changed content")
			}
			if !failState && r.state.Status != model.AnalysisFailed {
				t.Fatal("failure status was not saved")
			}
			if err := s.Process(context.Background(), event); err != nil || *calls != 1 {
				t.Fatal("redelivery regenerated failed work", err)
			}
		}
	}
}

func TestResummaryPublisherAndVerifiedDocumentPreparation(t *testing.T) {
	client := &actionEventStub{out: &eventbridge.PutEventsOutput{Entries: []ebtypes.PutEventsResultEntry{{EventId: aws.String("event")}}}}
	if err := ResummaryPublisher(client)(context.Background(), model.SummaryRequested{MeetingID: "meeting", RunID: "run"}); err != nil {
		t.Fatal(err)
	}
	entry := client.in.Entries[0]
	if aws.ToString(entry.Source) != "ttobak.analysis" || aws.ToString(entry.DetailType) != "SummaryRequested" ||
		aws.ToString(entry.Detail) != `{"meetingId":"meeting","runId":"run"}` {
		t.Fatal("event contains text or wrong routing")
	}
	client.out.FailedEntryCount = 1
	if err := ResummaryPublisher(client)(context.Background(), model.SummaryRequested{}); !errors.Is(err, ErrResummaryPublish) {
		t.Fatal(err)
	}

	text, repo, storage, _ := readingFixture(t)
	worker := &ResummaryService{repo: &resummaryTestRepo{}, attachments: text}
	snapshot := &model.SummarySnapshot{Meeting: repo.meetings[meetingKey("owner", "m")],
		Attachments: []model.Attachment{*repo.attachment}, TextStates: map[string]*model.AttachmentTextState{"a": repo.state}}
	if err := validateResummarySource(snapshot); err != nil {
		t.Fatal(err)
	}
	prepared, attachments, bindings, err := worker.prepare(context.Background(), snapshot)
	if err != nil || prepared.Content != snapshot.Meeting.Content || len(attachments) != 1 || attachments[0].ExtractedText == nil ||
		attachments[0].ExtractedText.Units[0].Text != "한국어 문서" || len(bindings) != 1 || bindings[0].ETag != repo.state.SourceETag {
		t.Fatalf("%+v %v", attachments, err)
	}
	storage.etag = `"replaced"`
	_, omitted, remainingBindings, err := worker.prepare(context.Background(), snapshot)
	if err != nil || !omitted[0].SummaryOmitted || omitted[0].ExtractedText != nil || len(remainingBindings) != 0 {
		t.Fatalf("unverified document was not omitted: %+v %v", omitted, err)
	}
	repo.state.Status = model.AttachmentTextFailed
	if err := validateResummarySource(snapshot); err != nil {
		t.Fatal("failed document blocked trusted saved notes")
	}
	snapshot.Meeting.Content = ""
	if err := validateResummarySource(snapshot); !errors.Is(err, ErrResummarySourcePending) {
		t.Fatal("failed document alone became a valid source")
	}
}

type ownerContinuityRepo struct {
	*resummaryTestRepo
	snapshots []*model.SummarySnapshot
	complete  func(context.Context, *model.SummarySnapshot, *model.ResummaryState, string, string, string, time.Time) error
}

func (r *ownerContinuityRepo) CaptureResummary(ctx context.Context, owner, mid, requester string) (*model.SummarySnapshot, error) {
	snapshot, err := r.resummaryTestRepo.CaptureResummary(ctx, owner, mid, requester)
	if snapshot != nil {
		snapshot.Checks[0].Fields["actionItems"] = model.SummaryValue{Present: true, Value: snapshot.Meeting.ActionItems}
		snapshot.Stored["actionItems"] = snapshot.Meeting.ActionItems
		snapshot.Checks = append(snapshot.Checks, model.SummaryCheck{PK: "MEETING#" + mid, SK: model.ActionAnalysisSK})
		r.snapshots = append(r.snapshots, snapshot)
	}
	return snapshot, err
}
func (r *ownerContinuityRepo) CompleteResummary(ctx context.Context, snapshot *model.SummarySnapshot, state *model.ResummaryState, content, coverage, hash string, now time.Time) error {
	if err := r.complete(ctx, snapshot, state, content, coverage, hash, now); err != nil {
		return err
	}
	return r.resummaryTestRepo.CompleteResummary(ctx, snapshot, state, content, coverage, hash, now)
}

func TestSharingSafeOwnerLegacyTaskContinuity(t *testing.T) {
	carried := map[string][]model.ActionItemContinuity{strings.Repeat("a", 64): make([]model.ActionItemContinuity, 500)}
	for i := range carried[strings.Repeat("a", 64)] {
		carried[strings.Repeat("a", 64)][i].ID = "existing"
	}
	for _, tc := range []struct {
		name, prior string
		carried     map[string][]model.ActionItemContinuity
		rejected    bool
		omitLast    bool
	}{
		{name: "legacy-done", prior: `[{"text":"PRIVATE_OLD_TASK","done":true}]`},
		{name: "completed-overrides-done", prior: `[{"text":"PRIVATE_OLD_TASK","done":true,"completed":false}]`},
		{name: "already-id", prior: `[{"id":"stable","text":"PRIVATE_OLD_TASK","completed":true}]`},
		{name: "mixed-and-duplicate-index", prior: `[{"id":"stable","text":"known","done":true},{"text":"PRIVATE_OLD_TASK","done":true},{"text":"PRIVATE_OLD_TASK","completed":false}]`},
		{name: "omitted-private-prior", prior: `[{"text":"returned","done":true},{"text":"PRIVATE_OMITTED","done":true}]`, omitLast: true},
		{name: "oversized-id", prior: `[{"id":"` + strings.Repeat("a", 129) + `","text":"task"}]`, rejected: true},
		{name: "malformed-carried-hash", prior: "[]", carried: map[string][]model.ActionItemContinuity{"bad": {{ID: "existing"}}}, rejected: true},
		{name: "missing-carried-id", prior: "[]", carried: map[string][]model.ActionItemContinuity{strings.Repeat("a", 64): {{}}}, rejected: true},
		{name: "combined-continuity-limit", prior: `[{"text":"task"}]`, carried: carried, rejected: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SUMMARY_PRIVACY_WRITER_ENABLED", "1")
			s, base, _ := newResummaryFixture(t)
			parent := base.meetings[meetingKey("owner", "meeting")]
			parent.SelectedTranscript, parent.TranscriptB, parent.ActionItems = "A", "", tc.prior
			parent.ActionItemContinuity = tc.carried
			old, err := storedActionItems(tc.prior)
			if err != nil {
				t.Fatal(err)
			}
			r := &ownerContinuityRepo{resummaryTestRepo: base}
			var continuity map[string][]model.ActionItemContinuity
			writes, modelCalls := 0, 0
			db := dynamodb.New(dynamodb.Options{Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, HTTPClient: noteSourceHTTPClient(func(req *http.Request) (*http.Response, error) {
				if req.Header.Get("X-Amz-Target") != "DynamoDB_20120810.TransactWriteItems" {
					t.Fatal("unexpected synthetic operation")
				}
				var body struct {
					TransactItems []struct {
						Update *struct {
							UpdateExpression          string
							ConditionExpression       string
							ExpressionAttributeNames  map[string]string
							ExpressionAttributeValues json.RawMessage
						}
					}
				}
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if len(body.TransactItems) != 4 {
					t.Fatal("publication/reset was not one four-item transaction")
				}
				op := body.TransactItems[0].Update
				values, err := attributevalue.UnmarshalMapJSON(op.ExpressionAttributeValues)
				if err != nil {
					t.Fatal(err)
				}
				for alias, field := range op.ExpressionAttributeNames {
					if field != "actionItemContinuity" {
						continue
					}
					tokens := strings.Fields(strings.ReplaceAll(op.UpdateExpression, ",", " "))
					for i := 0; i+2 < len(tokens); i++ {
						if tokens[i] == alias && tokens[i+1] == "=" {
							if err := attributevalue.Unmarshal(values[tokens[i+2]], &continuity); err != nil {
								t.Fatal(err)
							}
						}
					}
				}
				if continuity == nil {
					t.Fatal("missing persisted continuity")
				}
				casRaw := false
				for token, value := range values {
					var text string
					if attributevalue.Unmarshal(value, &text) == nil && text == tc.prior && strings.Contains(op.ConditionExpression, token) {
						casRaw = true
					}
				}
				encoded, err := json.Marshal(continuity)
				if err != nil || strings.Contains(string(encoded), "PRIVATE_OLD_TASK") || !casRaw {
					t.Fatal("continuity disclosed prior text or publication lost the raw task CAS")
				}
				writes++
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
			})})
			wire := repository.NewDynamoDBRepository(db, "synthetic")
			r.complete = func(ctx context.Context, snapshot *model.SummarySnapshot, state *model.ResummaryState, content, coverage, hash string, now time.Time) error {
				if snapshot.Stored["actionItems"] != tc.prior || snapshot.Checks[0].Fields["actionItems"].Value != tc.prior {
					t.Fatal("normalization changed raw stored/source CAS bytes")
				}
				return wire.CompleteResummary(ctx, snapshot, state, content, coverage, hash, now)
			}
			s.repo, s.meetings = r, &MeetingService{repo: r}
			s.generator = resummaryGenerateFunc(func(_ context.Context, meeting *model.Meeting, _ []model.Attachment) (string, error) {
				modelCalls++
				if meeting.Content != "" || meeting.ActionItems != "" || meeting.TranscriptA != "source A" {
					t.Fatal("private prior derivative reached model input")
				}
				return "new own evidence", nil
			})
			if _, err := s.RequestSharingSafe(context.Background(), "owner", "meeting"); err != nil {
				t.Fatal(err)
			}
			err = s.Process(context.Background(), model.SummaryRequested{MeetingID: "meeting", RunID: base.state.RunID})
			if tc.rejected {
				if !errors.Is(err, repository.ErrSummaryLimit) || writes != 0 || base.completes != 0 || base.state.ErrorCode != "OUTPUT_TOO_LARGE" || parent.Content != "old summary" {
					t.Fatal("normalization bypassed repository continuity limits", err)
				}
			} else if err != nil || writes != 1 {
				t.Fatal("valid continuity was not published once", err)
			}
			if modelCalls != 1 {
				t.Fatalf("writes=%d model=%d", writes, modelCalls)
			}
			if !tc.rejected {
				retained := old
				if tc.omitLast {
					retained = retained[:len(retained)-1]
				}
				generated := make([]ActionItem, len(retained))
				for i, item := range retained {
					generated[i] = ActionItem{Text: item.Text, Assignee: item.Assignee, DueDate: item.DueDate}
				}
				body, err := json.Marshal(generated)
				if err != nil {
					t.Fatal(err)
				}
				result, err := reconcileActionItemsWithContinuity("[]", string(body), continuity)
				if err != nil {
					t.Fatal(err)
				}
				items, err := storedActionItems(result)
				if err != nil || len(items) != len(retained) {
					t.Fatal("omitted private prior task was resurrected", err)
				}
				for i, item := range items {
					if item.ID != retained[i].ID || item.Completed != retained[i].Completed {
						t.Errorf("task%d lost stable identity/completion", i)
					}
				}
			}
			for _, snapshot := range r.snapshots {
				if snapshot.Meeting.ActionItems != tc.prior || snapshot.Stored["actionItems"] != tc.prior || snapshot.Checks[0].Fields["actionItems"].Value != tc.prior {
					t.Fatal("normalization mutated captured caller snapshot")
				}
			}
			if parent.ActionItems != tc.prior || !reflect.DeepEqual(parent.ActionItemContinuity, tc.carried) {
				t.Fatal("normalization mutated caller meeting")
			}
		})
	}
}

func TestSharingSafeCompletionTaskBounds(t *testing.T) {
	for _, tc := range []struct {
		name, prior string
		rejected    bool
	}{
		{"empty", "", false}, {"whitespace", " \n", false}, {"empty-array", "[]", false}, {"null-collection", "null", false},
		{"malformed", "[", true}, {"scalar", "true", true}, {"null-entry", "[null]", true},
		{"invalid-completed", `[{"completed":"true"}]`, true},
		{"oversized-whitespace", strings.Repeat(" ", 256*1024+1), true},
		{"too-many", "[" + strings.Repeat(`{"text":"task"},`, 500) + `{"text":"task"}]`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, r, _ := newResummaryFixture(t)
			meeting := r.meetings[meetingKey("owner", "meeting")]
			meeting.ActionItems = tc.prior
			meeting.SelectedTranscript, meeting.TranscriptB = "A", ""
			modelCalls := 0
			s.generator = resummaryGenerateFunc(func(_ context.Context, input *model.Meeting, _ []model.Attachment) (string, error) {
				modelCalls++
				if input.ActionItems != "" || input.Content != "" {
					t.Fatal("prior derivative entered sharing-safe evidence")
				}
				return "new own evidence", nil
			})
			t.Setenv("SUMMARY_PRIVACY_WRITER_ENABLED", "1")
			if _, err := s.RequestSharingSafe(context.Background(), "owner", "meeting"); err != nil {
				t.Fatal(err)
			}
			err := s.Process(context.Background(), model.SummaryRequested{MeetingID: "meeting", RunID: r.state.RunID})
			if tc.rejected {
				if !errors.Is(err, repository.ErrSummaryLimit) || r.completes != 0 || r.state.ErrorCode != "OUTPUT_TOO_LARGE" || meeting.Content != "old summary" {
					t.Fatal("invalid prior tasks bypassed fail-closed publication", err)
				}
			} else if err != nil || r.completes != 1 {
				t.Fatal("valid empty prior tasks blocked publication", err)
			}
			if modelCalls != 1 || meeting.ActionItems != tc.prior {
				t.Fatal("unexpected model calls or prior task mutation")
			}
		})
	}
}
