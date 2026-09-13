package service

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

var (
	ErrResummaryBusy          = errors.New("meeting transcription or summarization is active")
	ErrResummarySourcePending = errors.New("attachment text is not ready")
	ErrResummaryPublish       = errors.New("summary request delivery failed")
	resummaryID               = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
)

type resummaryRepo interface {
	CaptureResummary(context.Context, string, string, string) (*model.SummarySnapshot, error)
	GetResummary(context.Context, string) (*model.ResummaryState, error)
	QueueResummary(context.Context, *model.SummarySnapshot, *model.ResummaryState, *model.ResummaryState) error
	StartResummary(context.Context, *model.SummarySnapshot, *model.ResummaryState, time.Time, int64) error
	CompleteResummary(context.Context, *model.SummarySnapshot, *model.ResummaryState, string, string, string, time.Time) error
	FailResummary(context.Context, string, *model.ResummaryState, string, time.Time) error
	ReadResummaryTranscript(context.Context, string, string, string) (string, *model.SummaryObject, error)
	CheckResummaryObjects(context.Context, string, []model.SummaryObject) error
}
type resummaryGenerator interface {
	GenerateResummary(context.Context, *model.Meeting, []model.Attachment) (string, error)
}
type ResummaryResponse struct {
	Status     string     `json:"status"`
	RunID      string     `json:"runId,omitempty"`
	ErrorCode  string     `json:"errorCode,omitempty"`
	LeaseUntil int64      `json:"leaseUntil,omitempty"`
	UpdatedAt  *time.Time `json:"updatedAt,omitempty"`
	ResultHash string     `json:"resultHash,omitempty"`
}
type ResummaryService struct {
	repo        resummaryRepo
	meetings    *MeetingService
	generator   resummaryGenerator
	attachments *AttachmentTextService
	publish     func(context.Context, model.SummaryRequested) error
	now         func() time.Time
}

func NewResummaryService(repo resummaryRepo, meetings *MeetingService, generator resummaryGenerator, attachments *AttachmentTextService, publish func(context.Context, model.SummaryRequested) error) *ResummaryService {
	return &ResummaryService{repo: repo, meetings: meetings, generator: generator, attachments: attachments, publish: publish, now: time.Now}
}
func ResummaryPublisher(client actionEventClient) func(context.Context, model.SummaryRequested) error {
	return func(ctx context.Context, event model.SummaryRequested) error {
		if client == nil {
			return ErrResummaryPublish
		}
		body, err := json.Marshal(event)
		if err != nil {
			return ErrResummaryPublish
		}
		out, err := client.PutEvents(ctx, &eventbridge.PutEventsInput{Entries: []ebtypes.PutEventsRequestEntry{{
			Source: aws.String("ttobak.analysis"), DetailType: aws.String("SummaryRequested"), Detail: aws.String(string(body)),
		}}})
		if err != nil || out == nil || out.FailedEntryCount != 0 || len(out.Entries) != 1 || aws.ToString(out.Entries[0].ErrorCode) != "" || aws.ToString(out.Entries[0].EventId) == "" {
			return ErrResummaryPublish
		}
		return nil
	}
}
func summaryResponse(state *model.ResummaryState) *ResummaryResponse {
	if state == nil {
		return &ResummaryResponse{Status: model.AnalysisUnknown}
	}
	result := &ResummaryResponse{Status: state.Status, RunID: state.RunID, ErrorCode: state.ErrorCode, LeaseUntil: state.LeaseUntil}
	if !state.UpdatedAt.IsZero() {
		updated := state.UpdatedAt
		result.UpdatedAt = &updated
	}
	if state.Status == model.AnalysisSucceeded {
		result.ResultHash = state.ResultHash
	}
	return result
}
func resummaryHash(snapshot *model.SummarySnapshot) string {
	body, _ := json.Marshal(snapshot.Checks)
	return actionSourceHash(string(body))
}
func (s *ResummaryService) authorized(ctx context.Context, userID, meetingID string, edit bool) (*model.Meeting, error) {
	if !resummaryID.MatchString(meetingID) || !resummaryID.MatchString(userID) {
		return nil, ErrInvalidInput
	}
	meeting, permission, err := s.meetings.checkAccess(ctx, userID, meetingID)
	if err != nil {
		return nil, err
	}
	if meeting == nil {
		return nil, ErrNotFound
	}
	if edit && permission != "owner" && permission != model.PermissionEdit {
		return nil, ErrForbidden
	}
	return meeting, nil
}
func (s *ResummaryService) state(ctx context.Context, meetingID string) (*model.ResummaryState, error) {
	for i := 0; i < 3; i++ {
		state, err := s.repo.GetResummary(ctx, meetingID)
		if err != nil {
			return nil, err
		}
		if state.Pending() && state.LeaseUntil <= s.now().UnixMilli() {
			err = s.repo.FailResummary(ctx, meetingID, state, model.AnalysisInterrupted, s.now().UTC())
			if err != nil && !errors.Is(err, repository.ErrConditionFailed) {
				return nil, err
			}
			continue
		}
		return state, nil
	}
	return nil, repository.ErrConditionFailed
}
func (s *ResummaryService) Get(ctx context.Context, userID, meetingID string) (*ResummaryResponse, error) {
	if _, err := s.authorized(ctx, userID, meetingID, false); err != nil {
		return nil, err
	}
	state, err := s.state(ctx, meetingID)
	if err != nil {
		return nil, err
	}
	return summaryResponse(state), nil
}
func resummaryDocumentReady(meeting *model.Meeting, att *model.Attachment, state *model.AttachmentTextState) bool {
	return att.Status == model.AttachStatusDone && validAttachmentSource(att) && stateMatchesAttachment(state, meeting, att) &&
		(state.Status == model.AttachmentTextSucceeded || state.Status == model.AttachmentTextPartial) &&
		state.SourceETag != "" && resultRun(att, state.ResultKey) == state.RunID && state.UnitCount > 0
}

func validateResummarySource(snapshot *model.SummarySnapshot) error {
	if snapshot == nil || snapshot.Meeting == nil {
		return ErrNotFound
	}
	m := snapshot.Meeting
	if m.Status != model.StatusDone && m.Status != model.StatusError {
		return ErrResummaryBusy
	}
	transcript, _ := selectMeetingTranscript(m)
	hasSource := strings.TrimSpace(transcript) != "" || strings.TrimSpace(m.Notes) != "" || strings.TrimSpace(m.Content) != ""
	unavailableDocument := false
	for _, att := range snapshot.Attachments {
		if att.Type != model.AttachTypeDocument || attachmentFormat(att.OriginalKey) == "" {
			continue
		}
		state := snapshot.TextStates[att.AttachmentID]
		if !resummaryDocumentReady(m, &att, state) {
			unavailableDocument = true
			continue
		}
		hasSource = true
	}
	if !hasSource {
		if unavailableDocument {
			return ErrResummarySourcePending
		}
		return ErrResummaryNoSource
	}
	return validateMeetingNotes(m.Notes)
}
func (s *ResummaryService) Request(ctx context.Context, userID, meetingID string) (*ResummaryResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	meeting, err := s.authorized(ctx, userID, meetingID, true)
	if err != nil {
		return nil, err
	}
	prior, err := s.state(ctx, meetingID)
	if err != nil {
		return nil, err
	}
	if prior.Pending() {
		return summaryResponse(prior), nil
	}
	snapshot, err := s.repo.CaptureResummary(ctx, meeting.UserID, meetingID, userID)
	if err != nil {
		return nil, err
	}
	if err := validateResummarySource(snapshot); err != nil {
		return nil, err
	}
	now := s.now().UTC()
	next := &model.ResummaryState{RunID: uuid.NewString(), Status: model.AnalysisQueued, OwnerID: meeting.UserID, RequestedBy: userID,
		SourceHash: resummaryHash(snapshot), LeaseUntil: now.Add(5 * time.Minute).UnixMilli(), UpdatedAt: now}
	if err := s.repo.QueueResummary(ctx, snapshot, prior, next); err != nil {
		return nil, err
	}
	err = ErrResummaryPublish
	if s.publish != nil {
		err = s.publish(ctx, model.SummaryRequested{MeetingID: meetingID, RunID: next.RunID})
	}
	if err != nil {
		if failErr := s.fail(ctx, meetingID, next, model.AnalysisPublishFailed); failErr != nil {
			return nil, failErr
		}
		return nil, ErrResummaryPublish
	}
	return summaryResponse(next), nil
}
func (s *ResummaryService) fail(ctx context.Context, meetingID string, state *model.ResummaryState, code string) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err := s.repo.FailResummary(cleanup, meetingID, state, code, s.now().UTC())
	if errors.Is(err, repository.ErrConditionFailed) {
		return nil
	}
	return err
}
func resummaryErrorCode(err error) string {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "TIMEOUT"
	case errors.Is(err, ErrForbidden), errors.Is(err, repository.ErrSummaryAccess):
		return "ACCESS_REVOKED"
	case errors.Is(err, ErrNotFound):
		return "SOURCE_MISSING"
	case errors.Is(err, repository.ErrConditionFailed), errors.Is(err, ErrAttachmentSourceChanged):
		return model.AnalysisSourceChanged
	case errors.Is(err, repository.ErrSummaryLimit):
		return "SOURCE_TOO_LARGE"
	case errors.Is(err, ErrResummarySourcePending):
		return "SOURCE_NOT_READY"
	case errors.Is(err, ErrInvalidAnalysisResponse):
		return model.AnalysisInvalidOutput
	default:
		return "SOURCE_UNAVAILABLE"
	}
}

// boundResummaryDocument fairly allocates the existing encoded evidence budget
// across documents, retaining each verified location and explicitly excerpting.
func boundResummaryDocument(att model.Attachment, result *model.AttachmentTextResult, budget int) (model.Attachment, error) {
	copy := *result
	copy.Units = nil
	att.ExtractedText = &copy
	for _, unit := range result.Units {
		if len(copy.Units) == 50 {
			att.SummaryExcerpted = true
			break
		}
		runes := []rune(unit.Text)
		lo, hi := 0, len(runes)
		for lo < hi {
			n := (lo + hi + 1) / 2
			candidate := unit
			candidate.Text = string(runes[:n])
			copy.Units = append(copy.Units, candidate)
			size := len(documentEvidence(att))
			copy.Units = copy.Units[:len(copy.Units)-1]
			if size <= budget {
				lo = n
			} else {
				hi = n - 1
			}
		}
		if lo == 0 {
			att.SummaryExcerpted = true
			break
		}
		unit.Text = string(runes[:lo])
		copy.Units = append(copy.Units, unit)
		if lo < len(runes) {
			att.SummaryExcerpted = true
			break
		}
	}
	if len(copy.Units) == 0 {
		return att, repository.ErrSummaryLimit
	}
	return att, nil
}
func (s *ResummaryService) prepare(ctx context.Context, snapshot *model.SummarySnapshot) (*model.Meeting, []model.Attachment, []model.SummaryObject, error) {
	meeting := *snapshot.Meeting
	raw, variant := selectMeetingTranscript(&meeting)
	meeting.TranscriptA, meeting.TranscriptB, meeting.TranscriptSegments = "", "", ""
	var bindings []model.SummaryObject
	if raw != "" {
		field := "transcript" + variant
		text, binding, err := s.repo.ReadResummaryTranscript(ctx, meeting.MeetingID, field, raw)
		if err != nil {
			return nil, nil, nil, err
		}
		if binding != nil {
			bindings = append(bindings, *binding)
		}
		if variant == "B" {
			meeting.TranscriptB = text
		} else {
			meeting.TranscriptA = text
		}
		if snapshot.Meeting.TranscriptSegments != "" {
			segments, binding, err := s.repo.ReadResummaryTranscript(ctx, meeting.MeetingID, "transcriptSegments", snapshot.Meeting.TranscriptSegments)
			if err != nil {
				return nil, nil, nil, err
			}
			meeting.TranscriptSegments = segments
			if binding != nil {
				bindings = append(bindings, *binding)
			}
		}
	}
	documents := 0
	for _, att := range snapshot.Attachments {
		if att.Type == model.AttachTypeDocument && attachmentFormat(att.OriginalKey) != "" &&
			resummaryDocumentReady(snapshot.Meeting, &att, snapshot.TextStates[att.AttachmentID]) {
			documents++
		}
	}
	budget := 16 * 1024
	if documents > 0 {
		budget = min(budget, 64*1024/documents)
	}
	attachments := make([]model.Attachment, 0, len(snapshot.Attachments))
	for _, att := range snapshot.Attachments {
		if att.Type == model.AttachTypeDocument && attachmentFormat(att.OriginalKey) != "" {
			state := snapshot.TextStates[att.AttachmentID]
			if s.attachments == nil || !resummaryDocumentReady(snapshot.Meeting, &att, state) {
				att.SummaryOmitted = true
				attachments = append(attachments, att)
				continue
			}
			result, _, err := s.attachments.readResult(ctx, snapshot.Meeting, &att, state)
			if err != nil {
				att.SummaryOmitted = true
				attachments = append(attachments, att)
				continue
			}
			att, err = boundResummaryDocument(att, result, budget)
			if err != nil {
				att.ExtractedText = nil
				att.SummaryOmitted = true
				attachments = append(attachments, att)
				continue
			}
			att.ExtractedRevision = attachmentRevision(state)
			bindings = append(bindings, model.SummaryObject{Bucket: result.Source.Bucket, Key: result.Source.Key, ETag: result.Source.ETag})
		}
		attachments = append(attachments, att)
	}
	return &meeting, attachments, bindings, nil
}

func (s *ResummaryService) Process(ctx context.Context, event model.SummaryRequested) error {
	if !resummaryID.MatchString(event.MeetingID) || !resummaryID.MatchString(event.RunID) {
		return ErrInvalidInput
	}
	state, err := s.repo.GetResummary(ctx, event.MeetingID)
	if err != nil {
		return err
	}
	if state == nil || state.RunID != event.RunID || state.Status != model.AnalysisQueued {
		return nil
	}
	if state.LeaseUntil <= s.now().UnixMilli() {
		return s.fail(ctx, event.MeetingID, state, model.AnalysisInterrupted)
	}
	snapshot, err := s.repo.CaptureResummary(ctx, state.OwnerID, event.MeetingID, state.RequestedBy)
	if err == nil {
		err = validateResummarySource(snapshot)
	}
	if err == nil && resummaryHash(snapshot) != state.SourceHash {
		err = repository.ErrConditionFailed
	}
	if err != nil {
		return s.fail(ctx, event.MeetingID, state, resummaryErrorCode(err))
	}
	now := s.now().UTC()
	lease := now.Add(10 * time.Minute).UnixMilli()
	if err := s.repo.StartResummary(ctx, snapshot, state, now, lease); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return s.fail(ctx, event.MeetingID, state, model.AnalysisSourceChanged)
		}
		return err
	}
	running := *state
	running.Status = model.AnalysisRunning
	running.LeaseUntil = lease
	work, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	failed := func(code string) error { return s.fail(ctx, event.MeetingID, &running, code) }
	meeting, attachments, bindings, err := s.prepare(work, snapshot)
	if err != nil {
		return failed(resummaryErrorCode(err))
	}
	if err := s.repo.CheckResummaryObjects(work, event.MeetingID, bindings); err != nil {
		return failed(resummaryErrorCode(err))
	}
	fresh, err := s.repo.CaptureResummary(work, state.OwnerID, event.MeetingID, state.RequestedBy)
	if err != nil {
		return failed(resummaryErrorCode(err))
	}
	if fresh == nil || resummaryHash(fresh) != state.SourceHash {
		return failed(model.AnalysisSourceChanged)
	}
	if s.generator == nil {
		return failed(model.AnalysisProviderFailed)
	}
	content, err := s.generator.GenerateResummary(work, meeting, attachments)
	if err != nil {
		code := model.AnalysisProviderFailed
		if work.Err() != nil {
			code = "TIMEOUT"
		} else if errors.Is(err, ErrInvalidAnalysisResponse) {
			code = model.AnalysisInvalidOutput
		} else if errors.Is(err, ErrResummaryNoSource) {
			code = "NO_SUMMARY_SOURCE"
		}
		return failed(code)
	}
	if strings.TrimSpace(content) == "" {
		return failed(model.AnalysisInvalidOutput)
	}
	fresh, err = s.repo.CaptureResummary(work, state.OwnerID, event.MeetingID, state.RequestedBy)
	if err != nil {
		return failed(resummaryErrorCode(err))
	}
	if fresh == nil || resummaryHash(fresh) != state.SourceHash {
		return failed(model.AnalysisSourceChanged)
	}
	if err := s.repo.CheckResummaryObjects(work, event.MeetingID, bindings); err != nil {
		return failed(resummaryErrorCode(err))
	}
	snapshot.Objects = bindings
	err = s.repo.CompleteResummary(work, snapshot, &running, content, summaryAttachmentSnapshot(content, attachments), actionSourceHash(content), s.now().UTC())
	if err != nil {
		code := model.AnalysisPersistenceFail
		if errors.Is(err, repository.ErrConditionFailed) {
			code = model.AnalysisSourceChanged
		}
		if errors.Is(err, repository.ErrSummaryLimit) {
			code = "OUTPUT_TOO_LARGE"
		}
		return errors.Join(err, failed(code))
	}
	return nil
}
