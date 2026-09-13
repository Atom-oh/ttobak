package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
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
	for k, v := range map[string]interface{}{"content": m.Content, "notes": m.Notes, "transcriptA": m.TranscriptA, "transcriptB": m.TranscriptB, "selectedTranscript": m.SelectedTranscript, "transcriptSegments": m.TranscriptSegments, "status": m.Status} {
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
	event := model.SummaryRequested{MeetingID: "meeting", RunID: result.RunID}
	if err := s.Process(context.Background(), event); err != nil {
		t.Fatal(err)
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
	for _, kind := range []string{"content", "notes", "transcript", "revoked", "deleted"} {
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
			if err := s.Process(context.Background(), event); err != nil {
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
