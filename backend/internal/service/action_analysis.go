package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type actionAnalysisRepo interface {
	GetMeeting(context.Context, string, string) (*model.Meeting, error)
	GetActionAnalysis(context.Context, string) (*model.ActionItemsAnalysis, error)
	QueueActionAnalysis(context.Context, string, string, string, *model.ActionItemsAnalysis, *model.ActionItemsAnalysis) error
	StartActionAnalysis(context.Context, string, string, time.Time, int64) error
	FailActionAnalysis(context.Context, string, *model.ActionItemsAnalysis, string, time.Time) error
	CompleteActionAnalysis(context.Context, string, string, string, string, string, string, time.Time) error
	UpdateMeetingFieldsIfMatch(context.Context, string, string, map[string]interface{}, map[string]interface{}) error
}

type actionItemsExtractor interface {
	ExtractActionItemsForMeeting(context.Context, *model.Meeting) (string, error)
}

type ActionItemsResponse struct {
	ActionItems []ActionItem               `json:"actionItems"`
	Analysis    *model.ActionItemsAnalysis `json:"analysis"`
}

type ActionItemsAnalysisService struct {
	repo      actionAnalysisRepo
	meetings  *MeetingService
	extractor actionItemsExtractor
	publish   func(context.Context, model.ActionItemsRequested) error
	now       func() time.Time
}

func NewActionItemsAnalysisService(repo actionAnalysisRepo, meetings *MeetingService, extractor actionItemsExtractor, publish func(context.Context, model.ActionItemsRequested) error) *ActionItemsAnalysisService {
	return &ActionItemsAnalysisService{repo: repo, meetings: meetings, extractor: extractor, publish: publish, now: time.Now}
}

func actionSourceHash(source string) string {
	sum := sha256.Sum256([]byte(source))
	return hex.EncodeToString(sum[:])
}

func storedActionItems(raw string) ([]ActionItem, error) {
	items := []ActionItem{}
	var saved []struct {
		ActionItem
		Completed *bool `json:"completed"`
		Done      bool  `json:"done"`
	}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &saved); err != nil {
			return nil, fmt.Errorf("decode saved action items: %w", err)
		}
	}
	for i, entry := range saved {
		item := entry.ActionItem
		item.Completed = entry.Done
		if entry.Completed != nil {
			item.Completed = *entry.Completed
		}
		if item.ID == "" {
			// Preserve legacy entries without using an index-only identity
			// that could later address an entirely different task.
			identity, _ := json.Marshal([]string{fmt.Sprint(i), item.Text, item.Assignee, item.DueDate})
			item.ID = "legacy-" + actionSourceHash(string(identity))
		}
		items = append(items, item)
	}
	return items, nil
}

// Describe is only for callers that have already authorized this meeting.
func (s *ActionItemsAnalysisService) Describe(ctx context.Context, meeting *model.Meeting) (*model.ActionItemsAnalysis, error) {
	for attempt := 0; attempt < 3; attempt++ {
		state, err := s.repo.GetActionAnalysis(ctx, meeting.MeetingID)
		if err != nil {
			return nil, err
		}
		if state == nil {
			return &model.ActionItemsAnalysis{Status: model.AnalysisUnknown}, nil
		}
		if state.Pending() && state.LeaseUntil <= s.now().UnixMilli() {
			err = s.repo.FailActionAnalysis(ctx, meeting.MeetingID, state, model.AnalysisInterrupted, s.now())
			if err != nil && !errors.Is(err, repository.ErrConditionFailed) {
				return nil, err
			}
			continue
		}
		return state, nil
	}
	return nil, repository.ErrConditionFailed
}

func (s *ActionItemsAnalysisService) response(ctx context.Context, meeting *model.Meeting) (*ActionItemsResponse, error) {
	state, err := s.Describe(ctx, meeting)
	if err != nil {
		return nil, err
	}
	// Read items AFTER status using the repository's strongly consistent read.
	// Otherwise a completion between the two reads can pair "succeeded" with
	// the pre-completion empty array.
	fresh, err := s.repo.GetMeeting(ctx, meeting.UserID, meeting.MeetingID)
	if err != nil {
		return nil, err
	}
	if fresh == nil {
		return nil, ErrNotFound
	}
	if state.Status == model.AnalysisSucceeded && state.SourceHash != actionSourceHash(fresh.Content) {
		state.Status, state.ErrorCode = model.AnalysisFailed, model.AnalysisSourceChanged
	}
	items, err := storedActionItems(fresh.ActionItems)
	if err != nil {
		return nil, err
	}
	return &ActionItemsResponse{ActionItems: items, Analysis: state}, nil
}

func (s *ActionItemsAnalysisService) Get(ctx context.Context, userID, meetingID string) (*ActionItemsResponse, error) {
	meeting, _, err := s.meetings.checkAccess(ctx, userID, meetingID)
	if err != nil {
		return nil, err
	}
	if meeting == nil {
		return nil, ErrNotFound
	}
	return s.response(ctx, meeting)
}

func (s *ActionItemsAnalysisService) editable(ctx context.Context, userID, meetingID string) (*model.Meeting, error) {
	meeting, permission, err := s.meetings.checkAccess(ctx, userID, meetingID)
	if err != nil {
		return nil, err
	}
	if meeting == nil {
		return nil, ErrNotFound
	}
	if permission != "owner" && permission != "edit" {
		return nil, ErrForbidden
	}
	return meeting, nil
}

// queue returns claimed=false for an active run; repeated clicks do not create
// another model call. The repository also checks the summary/status atomically.
func (s *ActionItemsAnalysisService) queue(ctx context.Context, meeting *model.Meeting) (*model.ActionItemsAnalysis, bool, error) {
	if meeting.Status != model.StatusDone || strings.TrimSpace(meeting.Content) == "" {
		return nil, false, ErrNoAnalysisSource
	}
	prior, err := s.repo.GetActionAnalysis(ctx, meeting.MeetingID)
	if err != nil {
		return nil, false, err
	}
	now := s.now()
	if prior.Pending() && prior.LeaseUntil > now.UnixMilli() {
		return prior, false, nil
	}
	next := &model.ActionItemsAnalysis{
		RunID: uuid.NewString(), Status: model.AnalysisQueued, SourceHash: actionSourceHash(meeting.Content),
		StartedAt: now, UpdatedAt: now, LeaseUntil: now.Add(5 * time.Minute).UnixMilli(),
	}
	if err := s.repo.QueueActionAnalysis(ctx, meeting.UserID, meeting.MeetingID, meeting.Content, prior, next); err != nil {
		return nil, false, err
	}
	return next, true, nil
}

func (s *ActionItemsAnalysisService) Request(ctx context.Context, userID, meetingID string) (*ActionItemsResponse, error) {
	meeting, err := s.editable(ctx, userID, meetingID)
	if err != nil {
		return nil, err
	}
	state, claimed, err := s.queue(ctx, meeting)
	if err != nil {
		return nil, err
	}
	if claimed {
		err = s.publish(ctx, model.ActionItemsRequested{OwnerID: meeting.UserID, MeetingID: meetingID, RunID: state.RunID})
		if err != nil {
			if failErr := s.fail(ctx, meetingID, state, model.AnalysisPublishFailed); failErr != nil {
				return nil, errors.Join(err, failErr)
			}
			return nil, fmt.Errorf("publish action analysis: %w", err)
		}
	}
	return s.response(ctx, meeting)
}

func (s *ActionItemsAnalysisService) RunInline(ctx context.Context, ownerID, meetingID string) error {
	meeting, err := s.repo.GetMeeting(ctx, ownerID, meetingID)
	if err != nil {
		return err
	}
	if meeting == nil {
		return ErrNotFound
	}
	state, claimed, err := s.queue(ctx, meeting)
	if err != nil || !claimed {
		return err
	}
	return s.Process(ctx, model.ActionItemsRequested{OwnerID: ownerID, MeetingID: meetingID, RunID: state.RunID})
}

func (s *ActionItemsAnalysisService) fail(ctx context.Context, meetingID string, state *model.ActionItemsAnalysis, code string) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	err := s.repo.FailActionAnalysis(cleanup, meetingID, state, code, s.now())
	if errors.Is(err, repository.ErrConditionFailed) {
		return nil // A replacement run, deletion, or another terminal write won.
	}
	return err
}

func (s *ActionItemsAnalysisService) Process(ctx context.Context, event model.ActionItemsRequested) error {
	if event.OwnerID == "" || event.MeetingID == "" || event.RunID == "" {
		return ErrInvalidInput
	}
	state, err := s.repo.GetActionAnalysis(ctx, event.MeetingID)
	if err != nil {
		return err
	}
	if state == nil || state.RunID != event.RunID || state.Status != model.AnalysisQueued {
		return nil
	}
	meeting, err := s.repo.GetMeeting(ctx, event.OwnerID, event.MeetingID)
	if err != nil {
		return err
	}
	if meeting == nil || meeting.Status != model.StatusDone || state.SourceHash != actionSourceHash(meeting.Content) {
		return s.fail(ctx, event.MeetingID, state, model.AnalysisSourceChanged)
	}
	now := s.now()
	if state.LeaseUntil <= now.UnixMilli() {
		return s.fail(ctx, event.MeetingID, state, model.AnalysisInterrupted)
	}
	lease := now.Add(5 * time.Minute).UnixMilli()
	if err := s.repo.StartActionAnalysis(ctx, event.MeetingID, event.RunID, now, lease); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return nil
		}
		return err
	}
	state.Status, state.LeaseUntil = model.AnalysisRunning, lease
	// Copy before invoking the extractor. Neither reloads nor mutable aliases
	// may substitute a newer source under this run's hash.
	snapshot := *meeting
	budget := 2 * time.Minute
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline)-5*time.Second < budget {
		budget = time.Until(deadline) - 5*time.Second
	}
	if budget <= 0 {
		return s.fail(ctx, event.MeetingID, state, model.AnalysisInterrupted)
	}
	modelCtx, cancel := context.WithTimeout(ctx, budget)
	result, err := s.extractor.ExtractActionItemsForMeeting(modelCtx, &snapshot)
	cancel()
	if err != nil {
		code := model.AnalysisProviderFailed
		if errors.Is(err, ErrInvalidAnalysisResponse) {
			code = model.AnalysisInvalidOutput
		}
		return s.fail(ctx, event.MeetingID, state, code)
	}
	result, err = reconcileActionItems(snapshot.ActionItems, result)
	if err != nil {
		return s.fail(ctx, event.MeetingID, state, model.AnalysisInvalidOutput)
	}
	err = s.repo.CompleteActionAnalysis(ctx, event.OwnerID, event.MeetingID, event.RunID, snapshot.Content, snapshot.ActionItems, result, s.now())
	if errors.Is(err, repository.ErrConditionFailed) {
		return s.fail(ctx, event.MeetingID, state, model.AnalysisSourceChanged)
	}
	if err != nil {
		return errors.Join(err, s.fail(ctx, event.MeetingID, state, model.AnalysisPersistenceFail))
	}
	return nil
}

func reconcileActionItems(previous, generated string) (string, error) {
	old, err := storedActionItems(previous)
	if err != nil {
		return "", err
	}
	items, err := parseActionItems(generated)
	if err != nil {
		return "", err
	}
	type identity struct{ text, assignee, dueDate string }
	key := func(item ActionItem) identity {
		normalize := func(value string) string { return strings.Join(strings.Fields(value), " ") }
		return identity{normalize(item.Text), normalize(item.Assignee), item.DueDate}
	}
	byKey := make(map[identity][]ActionItem)
	used := make(map[string]bool)
	for _, item := range old {
		if item.ID != "" {
			byKey[key(item)] = append(byKey[key(item)], item)
		}
	}
	for i := range items {
		items[i].ID, items[i].Completed = uuid.NewString(), false
		matches := byKey[key(items[i])]
		for len(matches) > 0 {
			match := matches[0]
			matches = matches[1:]
			if !used[match.ID] {
				items[i].ID, items[i].Completed = match.ID, match.Completed
				used[match.ID] = true
				break
			}
		}
		byKey[key(items[i])] = matches
	}
	data, err := json.Marshal(items)
	return string(data), err
}

func (s *ActionItemsAnalysisService) SetCompleted(ctx context.Context, userID, meetingID, itemID string, completed bool) (*ActionItemsResponse, error) {
	for attempt := 0; attempt < 3; attempt++ {
		meeting, err := s.editable(ctx, userID, meetingID)
		if err != nil {
			return nil, err
		}
		items, err := storedActionItems(meeting.ActionItems)
		if err != nil {
			return nil, err
		}
		found := false
		for i := range items {
			if items[i].ID == itemID {
				items[i].Completed, found = completed, true
			}
		}
		if !found {
			return nil, ErrNotFound
		}
		data, err := json.Marshal(items)
		if err != nil {
			return nil, err
		}
		err = s.repo.UpdateMeetingFieldsIfMatch(ctx, meeting.UserID, meetingID,
			map[string]interface{}{"actionItems": meeting.ActionItems}, map[string]interface{}{"actionItems": string(data)})
		if errors.Is(err, repository.ErrConditionFailed) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return s.Get(ctx, userID, meetingID)
	}
	return nil, repository.ErrConditionFailed
}
