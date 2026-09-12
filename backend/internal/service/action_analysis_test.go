package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type analysisTestRepo struct {
	*mockMeetingRepo
	state                      *model.ActionItemsAnalysis
	failWrite, completionError error
}

func (r *analysisTestRepo) GetActionAnalysis(context.Context, string) (*model.ActionItemsAnalysis, error) {
	if r.state == nil {
		return nil, nil
	}
	state := *r.state
	return &state, nil
}

func (r *analysisTestRepo) QueueActionAnalysis(_ context.Context, owner, id, source string, prior, next *model.ActionItemsAnalysis) error {
	meeting := r.meetings[meetingKey(owner, id)]
	if meeting == nil || meeting.Content != source || meeting.Status != model.StatusDone {
		return repository.ErrConditionFailed
	}
	if (prior == nil) != (r.state == nil) || (prior != nil && *prior != *r.state) {
		return repository.ErrConditionFailed
	}
	state := *next
	r.state = &state
	return nil
}

func (r *analysisTestRepo) StartActionAnalysis(_ context.Context, _, run string, now time.Time, lease int64) error {
	if r.state == nil || r.state.RunID != run || r.state.Status != model.AnalysisQueued || r.state.LeaseUntil <= now.UnixMilli() {
		return repository.ErrConditionFailed
	}
	r.state.Status, r.state.LeaseUntil = model.AnalysisRunning, lease
	r.state.StartedAt, r.state.UpdatedAt = now, now
	return nil
}

func (r *analysisTestRepo) FailActionAnalysis(_ context.Context, _ string, state *model.ActionItemsAnalysis, code string, now time.Time) error {
	if r.failWrite != nil {
		return r.failWrite
	}
	if r.state == nil || r.state.RunID != state.RunID || r.state.Status != state.Status || r.state.LeaseUntil != state.LeaseUntil {
		return repository.ErrConditionFailed
	}
	r.state.Status, r.state.ErrorCode, r.state.LeaseUntil = model.AnalysisFailed, code, 0
	r.state.UpdatedAt = now
	return nil
}

func (r *analysisTestRepo) CompleteActionAnalysis(_ context.Context, owner, id, run, source, old, result string, now time.Time) error {
	if r.completionError != nil {
		return r.completionError
	}
	m := r.meetings[meetingKey(owner, id)]
	if m == nil || m.Content != source || m.ActionItems != old || r.state == nil ||
		r.state.RunID != run || r.state.Status != model.AnalysisRunning || r.state.LeaseUntil <= now.UnixMilli() {
		return repository.ErrConditionFailed
	}
	m.ActionItems = result
	r.state.Status, r.state.ErrorCode, r.state.LeaseUntil = model.AnalysisSucceeded, "", 0
	return nil
}

func (r *analysisTestRepo) UpdateMeetingFieldsIfMatch(_ context.Context, owner, id string, expected map[string]interface{}, fields map[string]interface{}) error {
	m := r.meetings[meetingKey(owner, id)]
	if m == nil || m.ActionItems != expected["actionItems"] {
		return repository.ErrConditionFailed
	}
	m.ActionItems = fields["actionItems"].(string)
	return nil
}

type analysisExtractorFunc func(context.Context, *model.Meeting) (string, error)

func (f analysisExtractorFunc) ExtractActionItemsForMeeting(ctx context.Context, m *model.Meeting) (string, error) {
	return f(ctx, m)
}

func newAnalysisTest(t *testing.T, extract analysisExtractorFunc) (*ActionItemsAnalysisService, *analysisTestRepo, *model.Meeting) {
	t.Helper()
	repo := &analysisTestRepo{mockMeetingRepo: newMockMeetingRepo()}
	meeting := &model.Meeting{
		UserID: "owner", MeetingID: "meeting", Status: model.StatusDone,
		Content:     "제안서를 준비하기로 결정했다.",
		ActionItems: `[{"id":"old","text":"제안서 준비","completed":true,"priority":"high"}]`,
	}
	repo.addMeeting(meeting)
	s := NewActionItemsAnalysisService(repo, NewMeetingServiceForTest(repo), extract, func(context.Context, model.ActionItemsRequested) error { return nil })
	s.now = func() time.Time { return time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC) }
	return s, repo, meeting
}

func TestActionAnalysisEmptySuccessAndFailureAreDifferent(t *testing.T) {
	for _, tc := range []struct {
		name, result, status, code string
		err                        error
	}{
		{"empty", "[]", model.AnalysisSucceeded, "", nil},
		{"invalid", "", model.AnalysisFailed, model.AnalysisInvalidOutput, ErrInvalidAnalysisResponse},
		{"provider", "", model.AnalysisFailed, model.AnalysisProviderFailed, errors.New("provider unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, repo, m := newAnalysisTest(t, func(context.Context, *model.Meeting) (string, error) { return tc.result, tc.err })
			old := m.ActionItems
			if err := s.RunInline(context.Background(), "owner", "meeting"); err != nil {
				t.Fatal(err)
			}
			if repo.state.Status != tc.status || repo.state.ErrorCode != tc.code {
				t.Fatalf("unexpected state: %+v", repo.state)
			}
			if tc.err != nil && m.ActionItems != old {
				t.Fatal("failure replaced previous items")
			}
			if tc.err == nil && m.ActionItems != "[]" {
				t.Fatalf("valid empty result not saved: %s", m.ActionItems)
			}
		})
	}
}

func TestActionAnalysisAuthorizationAndSource(t *testing.T) {
	s, repo, m := newAnalysisTest(t, nil)
	repo.shares[shareKey("reader", "meeting")] = &model.Share{OwnerID: "owner", MeetingID: "meeting", SharedToID: "reader", Permission: "view"}
	if _, err := s.Request(context.Background(), "reader", "meeting"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("reader retry: %v", err)
	}
	if _, err := s.SetCompleted(context.Background(), "reader", "meeting", "old", false); !errors.Is(err, ErrForbidden) {
		t.Fatalf("reader completion: %v", err)
	}
	if _, err := s.Get(context.Background(), "stranger", "meeting"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stranger read: %v", err)
	}
	m.Content = ""
	if _, err := s.Request(context.Background(), "owner", "meeting"); !errors.Is(err, ErrNoAnalysisSource) {
		t.Fatalf("missing source: %v", err)
	}
	if repo.state != nil {
		t.Fatal("unauthorized/missing-source request queued a run")
	}
}

func TestActionAnalysisDuplicateAndStaleEvents(t *testing.T) {
	calls := 0
	s, repo, _ := newAnalysisTest(t, func(context.Context, *model.Meeting) (string, error) {
		calls++
		return "[]", nil
	})
	if _, err := s.Request(context.Background(), "owner", "meeting"); err != nil {
		t.Fatal(err)
	}
	event := model.ActionItemsRequested{OwnerID: "owner", MeetingID: "meeting", RunID: repo.state.RunID}
	for i := 0; i < 2; i++ {
		if err := s.Process(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Request(context.Background(), "owner", "meeting"); err != nil {
		t.Fatal(err)
	}
	if err := s.Process(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || repo.state.Status != model.AnalysisQueued {
		t.Fatalf("duplicate/old event ran: calls=%d state=%+v", calls, repo.state)
	}
}

func TestActionAnalysisConcurrentChangesWin(t *testing.T) {
	for _, change := range []string{"source", "checkbox", "deleted", "new-run"} {
		t.Run(change, func(t *testing.T) {
			s, repo, m := newAnalysisTest(t, nil)
			s.extractor = analysisExtractorFunc(func(_ context.Context, snapshot *model.Meeting) (string, error) {
				if snapshot == m || snapshot.Content != m.Content {
					t.Fatal("extractor did not receive frozen snapshot")
				}
				switch change {
				case "source":
					m.Content = "새로운 회의록"
				case "checkbox":
					m.ActionItems = `[{"id":"old","text":"제안서 준비","completed":false}]`
				case "deleted":
					delete(repo.meetings, meetingKey("owner", "meeting"))
				case "new-run":
					repo.state.RunID = "replacement"
				}
				return "[]", nil
			})
			if err := s.RunInline(context.Background(), "owner", "meeting"); err != nil {
				t.Fatal(err)
			}
			if m.ActionItems == "[]" {
				t.Fatal("stale result overwrote newer state")
			}
			if change == "new-run" && repo.state.Status != model.AnalysisRunning {
				t.Fatal("old worker changed replacement state")
			}
		})
	}
}

func TestActionAnalysisExpiryPublishAndPersistenceFailure(t *testing.T) {
	s, repo, _ := newAnalysisTest(t, nil)
	s.publish = func(context.Context, model.ActionItemsRequested) error { return errors.New("event publish failed") }
	if _, err := s.Request(context.Background(), "owner", "meeting"); err == nil {
		t.Fatal("publish failure hidden")
	}
	if repo.state.ErrorCode != model.AnalysisPublishFailed {
		t.Fatalf("publish failure missing: %+v", repo.state)
	}
	repo.state.Status, repo.state.LeaseUntil = model.AnalysisRunning, s.now().Add(-time.Second).UnixMilli()
	got, err := s.Get(context.Background(), "owner", "meeting")
	if err != nil || got.Analysis.ErrorCode != model.AnalysisInterrupted {
		t.Fatalf("expired run not exposed: %+v %v", got, err)
	}
	repo.failWrite = errors.New("storage unavailable")
	if _, err := s.Request(context.Background(), "owner", "meeting"); !errors.Is(err, repo.failWrite) {
		t.Fatalf("failure persistence error swallowed: %v", err)
	}
}

func TestActionAnalysisStableIdentityAndCompletion(t *testing.T) {
	s, _, m := newAnalysisTest(t, func(context.Context, *model.Meeting) (string, error) {
		return `[{"id":"ai_1","text":"제안서 준비","completed":false,"priority":"low"},{"id":"old","text":"신규 작업","completed":true}]`, nil
	})
	if err := s.RunInline(context.Background(), "owner", "meeting"); err != nil {
		t.Fatal(err)
	}
	var items []ActionItem
	if err := json.Unmarshal([]byte(m.ActionItems), &items); err != nil {
		t.Fatal(err)
	}
	if items[0].ID != "old" || !items[0].Completed || items[1].ID == "old" || items[1].ID == "ai_1" || items[1].Completed {
		t.Fatalf("identity/completion not application-owned: %+v", items)
	}
	if _, err := s.SetCompleted(context.Background(), "owner", "meeting", items[1].ID, true); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(context.Background(), "owner", "meeting")
	if err != nil || len(got.ActionItems) != 2 || !got.ActionItems[1].Completed {
		t.Fatalf("completion did not persist: %+v %v", got, err)
	}
	if _, err := s.SetCompleted(context.Background(), "owner", "meeting", "removed-id", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old ID silently accepted: %v", err)
	}
}

func TestActionAnalysisUsesFreshSourceAfterAuthorization(t *testing.T) {
	s, repo, meeting := newAnalysisTest(t, nil)
	authorizedSnapshot := *meeting
	// A new summary/analysis completes after authorization's earlier read.
	meeting.Content = "새 요약"
	meeting.ActionItems = `[{"id":"new","text":"새 작업","completed":false}]`
	repo.state = &model.ActionItemsAnalysis{RunID: "new-run", Status: model.AnalysisSucceeded, SourceHash: actionSourceHash(meeting.Content)}
	got, err := s.response(context.Background(), &authorizedSnapshot)
	if err != nil || got.Analysis.Status != model.AnalysisSucceeded || len(got.ActionItems) != 1 || got.ActionItems[0].ID != "new" {
		t.Fatalf("fresh successful run misclassified: %+v %v", got, err)
	}
}

func TestActionAnalysisPreservesLegacyCompletionAndMissingIDs(t *testing.T) {
	s, _, meeting := newAnalysisTest(t, nil)
	meeting.ActionItems = `[{"text":"기존 완료","done":true},{"text":"기존 미완료","done":true,"completed":false}]`
	first, err := s.Get(context.Background(), "owner", "meeting")
	if err != nil || len(first.ActionItems) != 2 || !first.ActionItems[0].Completed || first.ActionItems[1].Completed || first.ActionItems[0].ID == first.ActionItems[1].ID {
		t.Fatalf("legacy data lost: %+v %v", first, err)
	}
	id := first.ActionItems[1].ID
	saved, err := s.SetCompleted(context.Background(), "owner", "meeting", id, true)
	if err != nil || !saved.ActionItems[0].Completed || !saved.ActionItems[1].Completed || saved.ActionItems[1].ID != id {
		t.Fatalf("legacy completion/identity did not survive saving: %+v %v", saved, err)
	}
}
