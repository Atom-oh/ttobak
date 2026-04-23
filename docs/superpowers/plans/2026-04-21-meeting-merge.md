# Meeting Merge Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Allow users to merge multiple meetings (split recordings or multi-device captures) into a single meeting with independent audio sources, per-source transcripts, and a unified Bedrock-generated summary.

**Architecture:** Add `AudioSource` nested struct to the Meeting model, replacing flat transcript/audio fields. Lazy migration on read preserves backward compatibility. New `POST /merge` API endpoint orchestrates data merging, source deletion, and async re-summarization. Frontend adds source tabs on the meeting detail page and a merge selection modal.

**Tech Stack:** Go (Lambda), DynamoDB single-table, Bedrock Claude Sonnet, Next.js 16 / React / TypeScript / Tailwind v4

---

## File Map

### Backend — New Files
- `backend/internal/service/merge.go` — MergeService with merge business logic
- `backend/internal/service/merge_test.go` — Tests for MergeService
- `backend/internal/handler/merge.go` — MergeMeetings HTTP handler
- `backend/internal/handler/merge_test.go` — Tests for merge handler

### Backend — Modified Files
- `backend/internal/model/meeting.go` — Add AudioSource struct, new fields on Meeting
- `backend/internal/model/request.go` — Add MergeMeetingsRequest, AudioSourceResponse, update MeetingDetailResponse
- `backend/internal/repository/dynamodb.go` — Add ReKeyAttachments, update resolveTranscripts for AudioSources
- `backend/internal/service/meeting.go` — Update GetMeetingDetail, UpdateSpeakers, CreateMeeting for AudioSources
- `backend/internal/service/bedrock.go` — Update SummarizeTranscript for multi-source prompt
- `backend/cmd/api/main.go` — Register merge route
- `backend/cmd/summarize/main.go` — Handle multi-source summarization trigger

### Frontend — New Files
- `frontend/src/components/meeting/AudioSourceTabs.tsx` — Tab bar for switching between audio sources
- `frontend/src/components/meeting/MergeMeetingModal.tsx` — Meeting selection modal for merge

### Frontend — Modified Files
- `frontend/src/types/meeting.ts` — Add AudioSource interface, update MeetingDetail
- `frontend/src/lib/api.ts` — Add mergeMeetings API call
- `frontend/src/app/meeting/[id]/MeetingDetailClient.tsx` — Integrate source tabs, merge button

---

## Task 1: Add AudioSource Model & Lazy Migration (Backend)

**Files:**
- Modify: `backend/internal/model/meeting.go:7-32`
- Modify: `backend/internal/model/request.go:96-118`

- [ ] **Step 1: Add AudioSource struct and update Meeting model**

In `backend/internal/model/meeting.go`, add the AudioSource struct after the existing Meeting struct, and add new fields to Meeting:

```go
// AudioSource represents an independent audio recording within a meeting.
// A meeting can have multiple sources (e.g., MacBook mic + iPhone mic for the same meeting).
type AudioSource struct {
	ID                 string            `dynamodbav:"id" json:"id"`
	Label              string            `dynamodbav:"label" json:"label"`
	AudioKey           string            `dynamodbav:"audioKey" json:"audioKey"`
	TranscriptA        string            `dynamodbav:"transcriptA,omitempty" json:"transcriptA,omitempty"`
	TranscriptB        string            `dynamodbav:"transcriptB,omitempty" json:"transcriptB,omitempty"`
	SelectedTranscript string            `dynamodbav:"selectedTranscript,omitempty" json:"selectedTranscript,omitempty"`
	TranscriptSegments string            `dynamodbav:"transcriptSegments,omitempty" json:"transcriptSegments,omitempty"`
	SpeakerMap         map[string]string `dynamodbav:"speakerMap,omitempty" json:"speakerMap,omitempty"`
	SttProvider        string            `dynamodbav:"sttProvider,omitempty" json:"sttProvider,omitempty"`
	Duration           float64           `dynamodbav:"duration,omitempty" json:"duration,omitempty"`
	CreatedAt          time.Time         `dynamodbav:"createdAt" json:"createdAt"`
}
```

Add these fields to the Meeting struct (keep existing flat fields for DynamoDB backward compat reads):

```go
// Add after existing Tags field:
AudioSources []AudioSource `dynamodbav:"audioSources,omitempty"`
MergedFrom   []string      `dynamodbav:"mergedFrom,omitempty"`
```

- [ ] **Step 2: Add EnsureAudioSources lazy migration method**

In `backend/internal/model/meeting.go`, add at the end:

```go
// EnsureAudioSources migrates legacy flat fields into AudioSources[0] on read.
// Call this after unmarshaling a Meeting from DynamoDB.
func (m *Meeting) EnsureAudioSources() {
	if len(m.AudioSources) > 0 {
		return
	}
	if m.AudioKey == "" && m.TranscriptA == "" && m.TranscriptB == "" {
		return
	}
	m.AudioSources = []AudioSource{{
		ID:                 m.MeetingID + "-src0",
		Label:              "기본",
		AudioKey:           m.AudioKey,
		TranscriptA:        m.TranscriptA,
		TranscriptB:        m.TranscriptB,
		SelectedTranscript: m.SelectedTranscript,
		TranscriptSegments: m.TranscriptSegments,
		SpeakerMap:         m.SpeakerMap,
		SttProvider:        m.SttProvider,
		CreatedAt:          m.CreatedAt,
	}}
}

// SelectedSource returns the AudioSource by ID, or the first one if not found.
func (m *Meeting) SelectedSource(sourceID string) *AudioSource {
	for i := range m.AudioSources {
		if m.AudioSources[i].ID == sourceID {
			return &m.AudioSources[i]
		}
	}
	if len(m.AudioSources) > 0 {
		return &m.AudioSources[0]
	}
	return nil
}
```

- [ ] **Step 3: Update MeetingDetailResponse with AudioSources**

In `backend/internal/model/request.go`, add `AudioSourceResponse` and update `MeetingDetailResponse`:

```go
// AudioSourceResponse represents an audio source in API responses
type AudioSourceResponse struct {
	ID                 string            `json:"id"`
	Label              string            `json:"label"`
	AudioKey           string            `json:"audioKey,omitempty"`
	TranscriptA        string            `json:"transcriptA,omitempty"`
	TranscriptB        string            `json:"transcriptB,omitempty"`
	SelectedTranscript *string           `json:"selectedTranscript,omitempty"`
	Transcription      json.RawMessage   `json:"transcription,omitempty"`
	SpeakerMap         map[string]string `json:"speakerMap,omitempty"`
	SttProvider        string            `json:"sttProvider,omitempty"`
	Duration           float64           `json:"duration,omitempty"`
}

// MergeMeetingsRequest represents the request body for merging meetings
type MergeMeetingsRequest struct {
	SourceMeetingIDs []string `json:"sourceMeetingIds"`
}
```

Add `AudioSources` and `MergedFrom` fields to `MeetingDetailResponse`:

```go
// Add to MeetingDetailResponse struct after existing Shares field:
AudioSources []AudioSourceResponse `json:"audioSources,omitempty"`
MergedFrom   []string              `json:"mergedFrom,omitempty"`
```

- [ ] **Step 4: Verify Go compiles**

Run: `cd /home/ec2-user/ttobak/backend && /usr/local/go/bin/go build ./...`
Expected: Clean compile (no errors)

- [ ] **Step 5: Commit**

```bash
git add backend/internal/model/meeting.go backend/internal/model/request.go
git commit -m "feat(model): add AudioSource struct and lazy migration for meeting merge"
```

---

## Task 2: Update Repository Layer for AudioSources

**Files:**
- Modify: `backend/internal/repository/dynamodb.go:127-148` (resolveTranscripts)
- Modify: `backend/internal/repository/dynamodb.go:150-186` (CreateMeeting)
- Modify: `backend/internal/repository/dynamodb.go:188-217` (GetMeeting)
- Modify: `backend/internal/repository/dynamodb.go:219-258` (GetMeetingByID)

- [ ] **Step 1: Update resolveTranscripts to handle AudioSources**

Replace the existing `resolveTranscripts` function in `dynamodb.go`:

```go
func (r *DynamoDBRepository) resolveTranscripts(ctx context.Context, meeting *model.Meeting) error {
	if meeting == nil {
		return nil
	}

	// Resolve legacy flat fields
	var err error
	if strings.HasPrefix(meeting.TranscriptA, "s3://") {
		meeting.TranscriptA, err = r.loadTranscript(ctx, meeting.TranscriptA)
		if err != nil {
			return fmt.Errorf("failed to load transcriptA: %w", err)
		}
	}
	if strings.HasPrefix(meeting.TranscriptB, "s3://") {
		meeting.TranscriptB, err = r.loadTranscript(ctx, meeting.TranscriptB)
		if err != nil {
			return fmt.Errorf("failed to load transcriptB: %w", err)
		}
	}

	// Resolve AudioSource transcripts
	for i := range meeting.AudioSources {
		src := &meeting.AudioSources[i]
		if strings.HasPrefix(src.TranscriptA, "s3://") {
			src.TranscriptA, err = r.loadTranscript(ctx, src.TranscriptA)
			if err != nil {
				return fmt.Errorf("failed to load audioSource[%d].transcriptA: %w", i, err)
			}
		}
		if strings.HasPrefix(src.TranscriptB, "s3://") {
			src.TranscriptB, err = r.loadTranscript(ctx, src.TranscriptB)
			if err != nil {
				return fmt.Errorf("failed to load audioSource[%d].transcriptB: %w", i, err)
			}
		}
	}

	return nil
}
```

- [ ] **Step 2: Add EnsureAudioSources call to GetMeeting and GetMeetingByID**

After `resolveTranscripts` call in both `GetMeeting` (around line 212) and `GetMeetingByID` (around line 253), add:

```go
meeting.EnsureAudioSources()
```

This goes right after the `resolveTranscripts` call and before `return &meeting, nil`.

- [ ] **Step 3: Add ReKeyAttachments repository method**

Add to `dynamodb.go`:

```go
// ReKeyAttachments moves attachment records from one meeting to another.
// Updates the PK from MEETING#{oldMeetingID} to MEETING#{newMeetingID} and
// updates the meetingId attribute. Uses TransactWriteItems in batches of 50
// (each re-key is 2 operations: delete old + put new).
func (r *DynamoDBRepository) ReKeyAttachments(ctx context.Context, oldMeetingID, newMeetingID string) error {
	attachments, err := r.ListAttachments(ctx, oldMeetingID)
	if err != nil {
		return fmt.Errorf("failed to list attachments for re-key: %w", err)
	}
	if len(attachments) == 0 {
		return nil
	}

	// Process in batches of 50 (each attachment = 2 transact items: delete + put)
	batchSize := 50
	for i := 0; i < len(attachments); i += batchSize {
		end := i + batchSize
		if end > len(attachments) {
			end = len(attachments)
		}
		batch := attachments[i:end]

		var transactItems []types.TransactWriteItem
		for _, att := range batch {
			// Delete old record
			transactItems = append(transactItems, types.TransactWriteItem{
				Delete: &types.Delete{
					TableName: aws.String(r.tableName),
					Key: map[string]types.AttributeValue{
						"PK": &types.AttributeValueMemberS{Value: model.PrefixMeeting + oldMeetingID},
						"SK": &types.AttributeValueMemberS{Value: model.PrefixAttachment + att.AttachmentID},
					},
				},
			})

			// Create new record with updated meetingId
			att.PK = model.PrefixMeeting + newMeetingID
			att.MeetingID = newMeetingID
			item, err := attributevalue.MarshalMap(att)
			if err != nil {
				return fmt.Errorf("failed to marshal attachment: %w", err)
			}
			transactItems = append(transactItems, types.TransactWriteItem{
				Put: &types.Put{
					TableName: aws.String(r.tableName),
					Item:      item,
				},
			})
		}

		if len(transactItems) > 0 {
			_, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
				TransactItems: transactItems,
			})
			if err != nil {
				return fmt.Errorf("failed to re-key attachments batch: %w", err)
			}
		}
	}

	return nil
}
```

- [ ] **Step 4: Verify Go compiles**

Run: `cd /home/ec2-user/ttobak/backend && /usr/local/go/bin/go build ./...`
Expected: Clean compile

- [ ] **Step 5: Commit**

```bash
git add backend/internal/repository/dynamodb.go
git commit -m "feat(repo): support AudioSource transcripts and attachment re-keying"
```

---

## Task 3: Update Service Layer for AudioSources

**Files:**
- Modify: `backend/internal/service/meeting.go:126-196` (GetMeetingDetail)
- Modify: `backend/internal/service/meeting.go:244-280` (UpdateSpeakers)

- [ ] **Step 1: Update GetMeetingDetail to return AudioSources**

In `meeting.go`, update the `GetMeetingDetail` function. Replace the `transcription` and response building section (around lines 167-195):

```go
// Build audio source responses
var audioSourceResponses []model.AudioSourceResponse
for _, src := range meeting.AudioSources {
	var transcription json.RawMessage
	if src.TranscriptSegments != "" {
		transcription = json.RawMessage(src.TranscriptSegments)
	}
	var selectedTranscript *string
	if src.SelectedTranscript != "" {
		st := src.SelectedTranscript
		selectedTranscript = &st
	}
	audioSourceResponses = append(audioSourceResponses, model.AudioSourceResponse{
		ID:                 src.ID,
		Label:              src.Label,
		AudioKey:           src.AudioKey,
		TranscriptA:        src.TranscriptA,
		TranscriptB:        src.TranscriptB,
		SelectedTranscript: selectedTranscript,
		Transcription:      transcription,
		SpeakerMap:         src.SpeakerMap,
		SttProvider:        src.SttProvider,
		Duration:           src.Duration,
	})
}

// Parse transcript segments for backward compat (first source)
var transcription json.RawMessage
if len(meeting.AudioSources) > 0 && meeting.AudioSources[0].TranscriptSegments != "" {
	transcription = json.RawMessage(meeting.AudioSources[0].TranscriptSegments)
}

return &model.MeetingDetailResponse{
	MeetingID:          meeting.MeetingID,
	UserID:             meeting.UserID,
	Title:              meeting.Title,
	Date:               meeting.Date.Format(time.RFC3339),
	Status:             meeting.Status,
	Participants:       meeting.Participants,
	Content:            meeting.Content,
	Notes:              meeting.Notes,
	// Legacy flat fields from first source for backward compat
	TranscriptA:        firstSourceField(meeting.AudioSources, func(s model.AudioSource) string { return s.TranscriptA }),
	TranscriptB:        firstSourceField(meeting.AudioSources, func(s model.AudioSource) string { return s.TranscriptB }),
	SelectedTranscript: strPtr(firstSourceField(meeting.AudioSources, func(s model.AudioSource) string { return s.SelectedTranscript })),
	AudioKey:           firstSourceField(meeting.AudioSources, func(s model.AudioSource) string { return s.AudioKey }),
	Tags:               meeting.Tags,
	ActionItems:        toRawJSON(meeting.ActionItems),
	SpeakerMap:         firstSourceSpeakerMap(meeting.AudioSources),
	SttProvider:        firstSourceField(meeting.AudioSources, func(s model.AudioSource) string { return s.SttProvider }),
	Transcription:      transcription,
	Attachments:        attachmentResponses,
	Shares:             shareResponses,
	AudioSources:       audioSourceResponses,
	MergedFrom:         meeting.MergedFrom,
	CreatedAt:          meeting.CreatedAt.Format(time.RFC3339),
	UpdatedAt:          meeting.UpdatedAt.Format(time.RFC3339),
}, nil
```

Add helper functions at the bottom of `meeting.go`:

```go
func firstSourceField(sources []model.AudioSource, getter func(model.AudioSource) string) string {
	if len(sources) > 0 {
		return getter(sources[0])
	}
	return ""
}

func firstSourceSpeakerMap(sources []model.AudioSource) map[string]string {
	if len(sources) > 0 {
		return sources[0].SpeakerMap
	}
	return nil
}
```

- [ ] **Step 2: Update UpdateSpeakers to handle sourceId**

In `meeting.go`, update `UpdateSpeakers` to accept an optional `sourceID` parameter. Add `SourceID` to `UpdateSpeakersRequest` in `request.go`:

```go
type UpdateSpeakersRequest struct {
	SpeakerMap map[string]string `json:"speakerMap"`
	SourceID   string            `json:"sourceId,omitempty"`
}
```

Update the `UpdateSpeakers` method in `meeting.go` to apply replacements to the specific source:

```go
func (s *MeetingService) UpdateSpeakers(ctx context.Context, userID, meetingID string, req *model.UpdateSpeakersRequest) (*model.MeetingUpdateResponse, error) {
	meeting, permission, err := s.checkAccess(ctx, userID, meetingID)
	if err != nil {
		return nil, err
	}
	if meeting == nil {
		return nil, ErrNotFound
	}
	if permission != "owner" && permission != model.PermissionEdit {
		return nil, ErrForbidden
	}

	// Apply to specific source or all sources
	for i := range meeting.AudioSources {
		src := &meeting.AudioSources[i]
		if req.SourceID != "" && src.ID != req.SourceID {
			continue
		}
		for label, name := range req.SpeakerMap {
			if name == "" {
				continue
			}
			src.TranscriptA = strings.ReplaceAll(src.TranscriptA, label, name)
			src.TranscriptB = strings.ReplaceAll(src.TranscriptB, label, name)
			src.TranscriptSegments = strings.ReplaceAll(src.TranscriptSegments, label, name)
			if src.SpeakerMap == nil {
				src.SpeakerMap = make(map[string]string)
			}
			src.SpeakerMap[label] = name
		}
	}

	// Also apply to meeting-level content
	for label, name := range req.SpeakerMap {
		if name == "" {
			continue
		}
		meeting.Content = strings.ReplaceAll(meeting.Content, label, name)
		meeting.ActionItems = strings.ReplaceAll(meeting.ActionItems, label, name)
	}

	if err := s.repo.UpdateMeeting(ctx, meeting); err != nil {
		return nil, err
	}

	return &model.MeetingUpdateResponse{
		MeetingID: meeting.MeetingID,
		UpdatedAt: meeting.UpdatedAt.Format(time.RFC3339),
	}, nil
}
```

- [ ] **Step 3: Verify Go compiles**

Run: `cd /home/ec2-user/ttobak/backend && /usr/local/go/bin/go build ./...`
Expected: Clean compile

- [ ] **Step 4: Commit**

```bash
git add backend/internal/service/meeting.go backend/internal/model/request.go
git commit -m "feat(service): update meeting detail and speakers for AudioSources"
```

---

## Task 4: Create MergeService (Backend)

**Files:**
- Create: `backend/internal/service/merge.go`
- Create: `backend/internal/service/merge_test.go`

- [ ] **Step 1: Write the merge test**

Create `backend/internal/service/merge_test.go`:

```go
package service

import (
	"context"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

// mockMeetingRepo implements the subset of DynamoDBRepository needed by MergeService
type mockMeetingRepo struct {
	meetings    map[string]*model.Meeting   // "userID|meetingID" -> meeting
	attachments map[string][]model.Attachment // meetingID -> attachments
	shares      map[string][]model.Share      // meetingID -> shares
	deleted     []string                      // deleted meetingIDs
}

func newMockMeetingRepo() *mockMeetingRepo {
	return &mockMeetingRepo{
		meetings:    make(map[string]*model.Meeting),
		attachments: make(map[string][]model.Attachment),
		shares:      make(map[string][]model.Share),
	}
}

func (m *mockMeetingRepo) addMeeting(meeting *model.Meeting) {
	key := meeting.UserID + "|" + meeting.MeetingID
	m.meetings[key] = meeting
}

func TestMergeService_Validate(t *testing.T) {
	repo := newMockMeetingRepo()
	now := time.Now().UTC()

	target := &model.Meeting{
		MeetingID: "target-1",
		UserID:    "user-1",
		Title:     "Target Meeting",
		Status:    model.StatusDone,
		CreatedAt: now,
		AudioSources: []model.AudioSource{{
			ID:    "src-t1",
			Label: "MacBook",
			AudioKey: "audio/user-1/target-1/rec.webm",
		}},
	}
	source := &model.Meeting{
		MeetingID: "source-1",
		UserID:    "user-1",
		Title:     "Source Meeting",
		Status:    model.StatusDone,
		CreatedAt: now.Add(-time.Hour),
		AudioSources: []model.AudioSource{{
			ID:    "src-s1",
			Label: "iPhone",
			AudioKey: "audio/user-1/source-1/rec.webm",
		}},
	}
	repo.addMeeting(target)
	repo.addMeeting(source)

	tests := []struct {
		name       string
		userID     string
		targetID   string
		sourceIDs  []string
		wantErr    bool
		errContains string
	}{
		{
			name: "valid merge",
			userID: "user-1", targetID: "target-1", sourceIDs: []string{"source-1"},
			wantErr: false,
		},
		{
			name: "source equals target",
			userID: "user-1", targetID: "target-1", sourceIDs: []string{"target-1"},
			wantErr: true, errContains: "cannot merge a meeting with itself",
		},
		{
			name: "empty source list",
			userID: "user-1", targetID: "target-1", sourceIDs: []string{},
			wantErr: true, errContains: "at least one source",
		},
		{
			name: "source not found",
			userID: "user-1", targetID: "target-1", sourceIDs: []string{"nonexistent"},
			wantErr: true, errContains: "not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMerge(tt.userID, tt.targetID, tt.sourceIDs, repo.getMeetings(tt.userID, tt.sourceIDs))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errContains != "" && !containsStr(err.Error(), tt.errContains) {
					t.Fatalf("expected error containing %q, got %q", tt.errContains, err.Error())
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsSubstring(s, sub))
}

func containsSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func (m *mockMeetingRepo) getMeetings(userID string, meetingIDs []string) map[string]*model.Meeting {
	result := make(map[string]*model.Meeting)
	for _, id := range meetingIDs {
		key := userID + "|" + id
		if mtg, ok := m.meetings[key]; ok {
			result[id] = mtg
		}
	}
	return result
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/ec2-user/ttobak/backend && /usr/local/go/bin/go test ./internal/service/ -run TestMergeService_Validate -v`
Expected: FAIL — `validateMerge` not defined

- [ ] **Step 3: Write MergeService implementation**

Create `backend/internal/service/merge.go`:

```go
package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// MergeService handles meeting merge operations
type MergeService struct {
	repo           *repository.DynamoDBRepository
	meetingService *MeetingService
}

// NewMergeService creates a new merge service
func NewMergeService(repo *repository.DynamoDBRepository, meetingService *MeetingService) *MergeService {
	return &MergeService{repo: repo, meetingService: meetingService}
}

// validateMerge checks preconditions for a merge operation
func validateMerge(userID, targetID string, sourceIDs []string, sourceMeetings map[string]*model.Meeting) error {
	if len(sourceIDs) == 0 {
		return fmt.Errorf("at least one source meeting ID is required")
	}

	for _, sid := range sourceIDs {
		if sid == targetID {
			return fmt.Errorf("cannot merge a meeting with itself")
		}
	}

	for _, sid := range sourceIDs {
		m, ok := sourceMeetings[sid]
		if !ok || m == nil {
			return fmt.Errorf("source meeting not found: %s", sid)
		}
		if m.UserID != userID {
			return ErrForbidden
		}
		if m.Status != model.StatusDone {
			return fmt.Errorf("source meeting %s is not done (status: %s)", sid, m.Status)
		}
	}

	return nil
}

// MergeMeetings merges source meetings into the target meeting.
// Returns the updated target meeting.
func (s *MergeService) MergeMeetings(ctx context.Context, userID, targetID string, sourceIDs []string) (*model.Meeting, error) {
	// 1. Get target meeting
	target, err := s.repo.GetMeeting(ctx, userID, targetID)
	if err != nil {
		return nil, err
	}
	if target == nil {
		return nil, ErrNotFound
	}
	if target.UserID != userID {
		return nil, ErrForbidden
	}
	if target.Status != model.StatusDone {
		return nil, fmt.Errorf("target meeting is not done (status: %s)", target.Status)
	}

	// 2. Get all source meetings
	sourceMeetings := make(map[string]*model.Meeting)
	for _, sid := range sourceIDs {
		m, err := s.repo.GetMeeting(ctx, userID, sid)
		if err != nil {
			return nil, err
		}
		sourceMeetings[sid] = m
	}

	// 3. Validate
	if err := validateMerge(userID, targetID, sourceIDs, sourceMeetings); err != nil {
		return nil, err
	}

	// 4. Merge AudioSources
	target.EnsureAudioSources()
	for _, sid := range sourceIDs {
		src := sourceMeetings[sid]
		src.EnsureAudioSources()
		for i := range src.AudioSources {
			as := src.AudioSources[i]
			if as.Label == "" || as.Label == "기본" {
				as.Label = src.Title
			}
			target.AudioSources = append(target.AudioSources, as)
		}
	}

	// 5. Merge Notes
	var notesParts []string
	if target.Notes != "" {
		notesParts = append(notesParts, target.Notes)
	}
	for _, sid := range sourceIDs {
		src := sourceMeetings[sid]
		if src.Notes != "" {
			notesParts = append(notesParts, fmt.Sprintf("--- %s ---\n%s", src.Title, src.Notes))
		}
	}
	if len(notesParts) > 0 {
		target.Notes = strings.Join(notesParts, "\n\n")
	}

	// 6. Merge Participants (union, deduplicated)
	participantSet := make(map[string]bool)
	for _, p := range target.Participants {
		participantSet[p] = true
	}
	for _, sid := range sourceIDs {
		for _, p := range sourceMeetings[sid].Participants {
			participantSet[p] = true
		}
	}
	target.Participants = make([]string, 0, len(participantSet))
	for p := range participantSet {
		target.Participants = append(target.Participants, p)
	}

	// 7. Merge Tags (union, deduplicated)
	tagSet := make(map[string]bool)
	for _, t := range target.Tags {
		tagSet[t] = true
	}
	for _, sid := range sourceIDs {
		for _, t := range sourceMeetings[sid].Tags {
			tagSet[t] = true
		}
	}
	target.Tags = make([]string, 0, len(tagSet))
	for t := range tagSet {
		target.Tags = append(target.Tags, t)
	}

	// 8. Use earliest date
	for _, sid := range sourceIDs {
		src := sourceMeetings[sid]
		if src.Date.Before(target.Date) {
			target.Date = src.Date
		}
	}

	// 9. Record merge provenance
	target.MergedFrom = append(target.MergedFrom, sourceIDs...)

	// 10. Re-key attachments from source to target
	for _, sid := range sourceIDs {
		if err := s.repo.ReKeyAttachments(ctx, sid, targetID); err != nil {
			return nil, fmt.Errorf("failed to re-key attachments from %s: %w", sid, err)
		}
	}

	// 11. Merge shares (union, more permissive wins)
	targetShares, _ := s.repo.ListSharesForMeeting(ctx, targetID)
	existingShareMap := make(map[string]string) // sharedToID -> permission
	for _, sh := range targetShares {
		existingShareMap[sh.SharedToID] = sh.Permission
	}
	for _, sid := range sourceIDs {
		sourceShares, _ := s.repo.ListSharesForMeeting(ctx, sid)
		for _, sh := range sourceShares {
			existing, ok := existingShareMap[sh.SharedToID]
			if !ok || (existing == model.PermissionRead && sh.Permission == model.PermissionEdit) {
				if !ok {
					// Create new share on target
					s.repo.CreateShare(ctx, &model.Share{
						PK:         model.PrefixUser + sh.SharedToID,
						SK:         model.PrefixShare + targetID,
						MeetingID:  targetID,
						OwnerID:    userID,
						SharedToID: sh.SharedToID,
						Email:      sh.Email,
						Permission: sh.Permission,
						CreatedAt:  time.Now().UTC(),
						EntityType: "SHARE",
					})
				}
				existingShareMap[sh.SharedToID] = sh.Permission
			}
		}
	}

	// 12. Set status to summarizing for re-summarization
	target.Status = model.StatusSummarizing
	target.UpdatedAt = time.Now().UTC()

	// 13. Save target meeting
	if err := s.repo.UpdateMeeting(ctx, target); err != nil {
		return nil, fmt.Errorf("failed to update target meeting: %w", err)
	}

	// 14. Delete source meetings
	for _, sid := range sourceIDs {
		if err := s.repo.DeleteMeeting(ctx, userID, sid); err != nil {
			return nil, fmt.Errorf("failed to delete source meeting %s: %w", sid, err)
		}
	}

	return target, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/ec2-user/ttobak/backend && /usr/local/go/bin/go test ./internal/service/ -run TestMergeService_Validate -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add backend/internal/service/merge.go backend/internal/service/merge_test.go
git commit -m "feat(service): add MergeService for meeting merge operations"
```

---

## Task 5: Create Merge Handler & Route Registration

**Files:**
- Create: `backend/internal/handler/merge.go`
- Modify: `backend/cmd/api/main.go:109-130`

- [ ] **Step 1: Create merge handler**

Create `backend/internal/handler/merge.go`:

```go
package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

// MergeHandler handles meeting merge requests
type MergeHandler struct {
	mergeService   *service.MergeService
	meetingService *service.MeetingService
}

// NewMergeHandler creates a new merge handler
func NewMergeHandler(mergeService *service.MergeService, meetingService *service.MeetingService) *MergeHandler {
	return &MergeHandler{
		mergeService:   mergeService,
		meetingService: meetingService,
	}
}

// MergeMeetings handles POST /api/meetings/{meetingId}/merge
func (h *MergeHandler) MergeMeetings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	targetMeetingID := chi.URLParam(r, "meetingId")

	if targetMeetingID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Meeting ID is required")
		return
	}

	var req model.MergeMeetingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Invalid request body")
		return
	}

	if len(req.SourceMeetingIDs) == 0 {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "At least one source meeting ID is required")
		return
	}

	merged, err := h.mergeService.MergeMeetings(ctx, userID, targetMeetingID, req.SourceMeetingIDs)
	if err != nil {
		if errors.Is(err, service.ErrForbidden) {
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
			return
		}
		if errors.Is(err, service.ErrNotFound) {
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Meeting not found")
			return
		}
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, err.Error())
		return
	}

	// Return updated meeting detail
	result, err := h.meetingService.GetMeetingDetail(ctx, userID, merged.MeetingID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}
```

- [ ] **Step 2: Register route and initialize handler in main.go**

In `backend/cmd/api/main.go`, add service and handler initialization after the existing `meetingHandler` (around line 71):

```go
mergeService := service.NewMergeService(repo, meetingService)
mergeHandler := handler.NewMergeHandler(mergeService, meetingService)
```

Add the route inside the authenticated routes group (after the speakers route, around line 126):

```go
// Meeting merge
r.Post("/api/meetings/{meetingId}/merge", mergeHandler.MergeMeetings)
```

- [ ] **Step 3: Verify Go compiles**

Run: `cd /home/ec2-user/ttobak/backend && /usr/local/go/bin/go build ./...`
Expected: Clean compile

- [ ] **Step 4: Commit**

```bash
git add backend/internal/handler/merge.go backend/cmd/api/main.go
git commit -m "feat(api): add POST /api/meetings/{meetingId}/merge endpoint"
```

---

## Task 6: Update Bedrock Summarization for Multi-Source

**Files:**
- Modify: `backend/internal/service/bedrock.go:118-217`

- [ ] **Step 1: Update SummarizeTranscript to handle AudioSources**

In `bedrock.go`, update `SummarizeTranscript`. After fetching the meeting (around line 132), add multi-source handling before the single-transcript logic:

```go
// Multi-source merged meeting
if len(meeting.AudioSources) > 1 {
	return s.summarizeMultiSource(ctx, meeting)
}

// Single source — use legacy logic with AudioSources[0]
meeting.EnsureAudioSources()
if len(meeting.AudioSources) == 0 {
	return "", fmt.Errorf("no audio sources for meeting: %s", meetingID)
}
src := meeting.AudioSources[0]
transcript := src.TranscriptA
if src.SelectedTranscript == "B" && src.TranscriptB != "" {
	transcript = src.TranscriptB
} else if transcript == "" && src.TranscriptB != "" {
	transcript = src.TranscriptB
}
```

Then add the `summarizeMultiSource` method:

```go
func (s *BedrockService) summarizeMultiSource(ctx context.Context, meeting *model.Meeting) (string, error) {
	var sb strings.Builder
	sb.WriteString("다음은 같은 회의를 여러 기기에서 녹음한 전사 결과입니다.\n\n")

	for _, src := range meeting.AudioSources {
		transcript := src.TranscriptA
		if src.SelectedTranscript == "B" && src.TranscriptB != "" {
			transcript = src.TranscriptB
		} else if transcript == "" && src.TranscriptB != "" {
			transcript = src.TranscriptB
		}
		if transcript == "" {
			continue
		}

		label := src.Label
		if label == "" {
			label = src.ID
		}

		// Prefer speaker-labeled segments if available
		if src.TranscriptSegments != "" {
			var segments []speakerSegment
			if err := json.Unmarshal([]byte(src.TranscriptSegments), &segments); err == nil && len(segments) > 0 {
				sb.WriteString(fmt.Sprintf("[소스: %s]\n", label))
				for _, seg := range segments {
					sb.WriteString(fmt.Sprintf("[%s %.0f초~%.0f초] %s\n", seg.Speaker, seg.StartTime, seg.EndTime, seg.Text))
				}
				sb.WriteString("\n")
				continue
			}
		}

		sb.WriteString(fmt.Sprintf("[소스: %s]\n%s\n\n", label, transcript))
	}

	sb.WriteString("이 전사 결과들을 종합하여 하나의 통합 회의록을 작성하세요.\n중복되는 내용은 한 번만, 각 소스에서만 들린 내용도 포함하세요.")

	systemPrompt := `You are an expert meeting assistant. Create comprehensive, well-structured meeting notes in Markdown.
You are given transcripts from MULTIPLE recording devices capturing the SAME meeting.
Cross-reference the sources to produce one unified, coherent summary.

Your output MUST follow this exact structure:

# 회의록

## 참석자
- 화자별 식별 및 주요 역할 추정

## 개요
- 회의 핵심 요약 (3-5문장)

## 화자별 주요 발언
### [Speaker Label]
- 주요 발언 요약 (2-3개)

## 주요 논의 사항
- 논의된 핵심 토픽 (상세하게)

## 결정 사항
- 합의된 결정들

## 액션 아이템
- [ ] 담당자(Speaker Label): 할 일 내용

Format in Korean unless the transcript is entirely in English.
Use bullet points and checkboxes. Include timestamps where available.`

	request := ClaudeRequest{
		AnthropicVersion: "bedrock-2023-05-31",
		MaxTokens:        4096,
		System:           systemPrompt,
		Messages: []ClaudeMessage{
			{
				Role: "user",
				Content: []ContentBlock{
					{Type: "text", Text: sb.String()},
				},
			},
		},
	}

	content, err := s.invokeClaudeModelWithID(ctx, request, ClaudeSonnetModelID)
	if err != nil {
		return "", fmt.Errorf("failed to generate multi-source content: %w", err)
	}

	if err := s.repo.UpdateMeetingFields(ctx, meeting.UserID, meeting.MeetingID, map[string]interface{}{
		"content": content,
		"status":  model.StatusDone,
	}); err != nil {
		return "", fmt.Errorf("failed to update meeting: %w", err)
	}

	return content, nil
}
```

- [ ] **Step 2: Verify Go compiles**

Run: `cd /home/ec2-user/ttobak/backend && /usr/local/go/bin/go build ./...`
Expected: Clean compile

- [ ] **Step 3: Commit**

```bash
git add backend/internal/service/bedrock.go
git commit -m "feat(bedrock): add multi-source summarization for merged meetings"
```

---

## Task 7: Frontend Types & API Client

**Files:**
- Modify: `frontend/src/types/meeting.ts:101-114`
- Modify: `frontend/src/lib/api.ts`

- [ ] **Step 1: Add AudioSource type and update MeetingDetail**

In `frontend/src/types/meeting.ts`, add the AudioSource interface after TranscriptComparison (around line 82):

```typescript
export interface AudioSource {
  id: string;
  label: string;
  audioKey?: string;
  transcriptA?: string;
  transcriptB?: string;
  selectedTranscript?: 'A' | 'B' | null;
  transcription?: TranscriptSegment[];
  speakerMap?: Record<string, string>;
  sttProvider?: string;
  duration?: number;
}
```

Update the MeetingDetail interface to add:

```typescript
export interface MeetingDetail extends Meeting {
  content?: string;
  notes?: string;
  transcriptA?: string;
  transcriptB?: string;
  selectedTranscript?: 'A' | 'B' | null;
  audioKey?: string;
  speakerMap?: Record<string, string>;
  shares?: SharedUser[];
  isShared?: boolean;
  sharedBy?: string | null;
  permission?: 'read' | 'edit' | null;
  audioSources?: AudioSource[];
  mergedFrom?: string[];
}
```

- [ ] **Step 2: Add mergeMeetings API call**

In `frontend/src/lib/api.ts`, add to the `meetingsApi` object:

```typescript
merge: (meetingId: string, sourceMeetingIds: string[]) =>
  api.post<import('@/types/meeting').MeetingDetail>(
    `/api/meetings/${meetingId}/merge`,
    { sourceMeetingIds }
  ),
```

- [ ] **Step 3: Verify frontend compiles**

Run: `cd /home/ec2-user/ttobak/frontend && npm run lint`
Expected: No new errors

- [ ] **Step 4: Commit**

```bash
git add frontend/src/types/meeting.ts frontend/src/lib/api.ts
git commit -m "feat(frontend): add AudioSource types and merge API client"
```

---

## Task 8: AudioSourceTabs Component

**Files:**
- Create: `frontend/src/components/meeting/AudioSourceTabs.tsx`

- [ ] **Step 1: Create AudioSourceTabs component**

Create `frontend/src/components/meeting/AudioSourceTabs.tsx`:

```tsx
'use client';

import { useState } from 'react';
import type { AudioSource } from '@/types/meeting';

interface AudioSourceTabsProps {
  sources: AudioSource[];
  activeSourceId: string;
  onSelectSource: (sourceId: string) => void;
  onMergeClick: () => void;
  onLabelChange?: (sourceId: string, label: string) => void;
}

export function AudioSourceTabs({
  sources,
  activeSourceId,
  onSelectSource,
  onMergeClick,
  onLabelChange,
}: AudioSourceTabsProps) {
  const [editingId, setEditingId] = useState<string | null>(null);
  const [editValue, setEditValue] = useState('');

  if (sources.length <= 1) {
    return (
      <div className="flex justify-end mb-3">
        <button
          onClick={onMergeClick}
          className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-gray-500 dark:text-gray-400 hover:text-primary dark:hover:text-cyan-400 rounded-lg hover:bg-gray-100 dark:hover:bg-white/5 transition-colors"
        >
          <span className="material-symbols-outlined text-base">call_merge</span>
          합치기
        </button>
      </div>
    );
  }

  const handleDoubleClick = (source: AudioSource) => {
    setEditingId(source.id);
    setEditValue(source.label);
  };

  const handleSaveLabel = (sourceId: string) => {
    if (editValue.trim() && onLabelChange) {
      onLabelChange(sourceId, editValue.trim());
    }
    setEditingId(null);
  };

  return (
    <div className="flex items-center gap-1 mb-3 overflow-x-auto scrollbar-hide">
      {sources.map((source) => (
        <button
          key={source.id}
          onClick={() => onSelectSource(source.id)}
          onDoubleClick={() => handleDoubleClick(source)}
          className={`flex-shrink-0 px-3 py-1.5 rounded-lg text-sm font-medium transition-colors ${
            source.id === activeSourceId
              ? 'bg-primary/10 text-primary dark:bg-cyan-500/10 dark:text-cyan-400'
              : 'text-gray-500 dark:text-gray-400 hover:bg-gray-100 dark:hover:bg-white/5'
          }`}
        >
          {editingId === source.id ? (
            <input
              autoFocus
              value={editValue}
              onChange={(e) => setEditValue(e.target.value)}
              onBlur={() => handleSaveLabel(source.id)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') handleSaveLabel(source.id);
                if (e.key === 'Escape') setEditingId(null);
              }}
              className="w-20 bg-transparent border-b border-primary outline-none text-sm"
              onClick={(e) => e.stopPropagation()}
            />
          ) : (
            source.label || `소스 ${sources.indexOf(source) + 1}`
          )}
        </button>
      ))}
      <button
        onClick={onMergeClick}
        className="flex-shrink-0 flex items-center gap-1 px-2 py-1.5 text-gray-400 dark:text-gray-500 hover:text-primary dark:hover:text-cyan-400 rounded-lg hover:bg-gray-100 dark:hover:bg-white/5 transition-colors"
        title="다른 미팅과 합치기"
      >
        <span className="material-symbols-outlined text-base">call_merge</span>
        <span className="text-xs hidden sm:inline">합치기</span>
      </button>
    </div>
  );
}
```

- [ ] **Step 2: Verify frontend compiles**

Run: `cd /home/ec2-user/ttobak/frontend && npm run lint`
Expected: No new errors

- [ ] **Step 3: Commit**

```bash
git add frontend/src/components/meeting/AudioSourceTabs.tsx
git commit -m "feat(ui): add AudioSourceTabs component for multi-source meetings"
```

---

## Task 9: MergeMeetingModal Component

**Files:**
- Create: `frontend/src/components/meeting/MergeMeetingModal.tsx`

- [ ] **Step 1: Create MergeMeetingModal component**

Create `frontend/src/components/meeting/MergeMeetingModal.tsx`:

```tsx
'use client';

import { useState, useEffect, useCallback } from 'react';
import { meetingsApi } from '@/lib/api';

interface MeetingListItem {
  meetingId: string;
  title: string;
  date: string;
  status: string;
}

interface MergeMeetingModalProps {
  isOpen: boolean;
  onClose: () => void;
  currentMeetingId: string;
  onMerge: (sourceMeetingIds: string[]) => Promise<void>;
}

export function MergeMeetingModal({
  isOpen,
  onClose,
  currentMeetingId,
  onMerge,
}: MergeMeetingModalProps) {
  const [meetings, setMeetings] = useState<MeetingListItem[]>([]);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [search, setSearch] = useState('');
  const [loading, setLoading] = useState(false);
  const [merging, setMerging] = useState(false);

  const fetchMeetings = useCallback(async () => {
    setLoading(true);
    try {
      const res = await meetingsApi.list({ tab: 'all' });
      const eligible = (res as { meetings: MeetingListItem[] }).meetings.filter(
        (m: MeetingListItem) => m.meetingId !== currentMeetingId && m.status === 'done'
      );
      setMeetings(eligible);
    } catch (err) {
      console.error('Failed to fetch meetings:', err);
    } finally {
      setLoading(false);
    }
  }, [currentMeetingId]);

  useEffect(() => {
    if (isOpen) {
      fetchMeetings();
      setSelected(new Set());
      setSearch('');
    }
  }, [isOpen, fetchMeetings]);

  const filteredMeetings = meetings.filter((m) =>
    m.title.toLowerCase().includes(search.toLowerCase())
  );

  const toggleSelect = (id: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) {
        next.delete(id);
      } else {
        next.add(id);
      }
      return next;
    });
  };

  const handleMerge = async () => {
    if (selected.size === 0) return;
    const confirmed = confirm(
      `선택한 ${selected.size}개 미팅을 이 미팅에 합칩니다. 원본 미팅은 삭제됩니다. 계속하시겠습니까?`
    );
    if (!confirmed) return;

    setMerging(true);
    try {
      await onMerge(Array.from(selected));
      onClose();
    } catch (err) {
      console.error('Merge failed:', err);
      alert('미팅 합치기에 실패했습니다.');
    } finally {
      setMerging(false);
    }
  };

  if (!isOpen) return null;

  return (
    <>
      {/* Backdrop */}
      <div className="fixed inset-0 bg-black/50 z-50" onClick={onClose} />

      {/* Modal — fullscreen bottom sheet on mobile, centered on desktop */}
      <div className="fixed inset-x-0 bottom-0 lg:inset-auto lg:top-1/2 lg:left-1/2 lg:-translate-x-1/2 lg:-translate-y-1/2 z-50 bg-white dark:bg-gray-900 rounded-t-2xl lg:rounded-2xl max-h-[80vh] lg:max-h-[70vh] lg:w-[480px] flex flex-col shadow-2xl">
        {/* Header */}
        <div className="flex items-center justify-between px-5 py-4 border-b border-gray-200 dark:border-white/10">
          <h2 className="text-lg font-bold text-gray-900 dark:text-white">미팅 합치기</h2>
          <button onClick={onClose} className="text-gray-400 hover:text-gray-600 dark:hover:text-gray-300">
            <span className="material-symbols-outlined">close</span>
          </button>
        </div>

        {/* Search */}
        <div className="px-5 py-3">
          <input
            type="text"
            placeholder="미팅 검색..."
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="w-full px-3 py-2 rounded-lg border border-gray-200 dark:border-white/10 bg-gray-50 dark:bg-white/5 text-sm text-gray-900 dark:text-white placeholder:text-gray-400 outline-none focus:border-primary dark:focus:border-cyan-400"
          />
        </div>

        {/* Meeting list */}
        <div className="flex-1 overflow-y-auto px-5 pb-3">
          {loading ? (
            <div className="flex items-center justify-center py-8">
              <div className="animate-spin rounded-full h-6 w-6 border-2 border-primary border-t-transparent" />
            </div>
          ) : filteredMeetings.length === 0 ? (
            <p className="text-center text-sm text-gray-400 py-8">합칠 수 있는 미팅이 없습니다</p>
          ) : (
            <div className="space-y-1.5">
              {filteredMeetings.map((m) => (
                <button
                  key={m.meetingId}
                  onClick={() => toggleSelect(m.meetingId)}
                  className={`w-full flex items-center gap-3 px-3 py-2.5 rounded-lg text-left transition-colors ${
                    selected.has(m.meetingId)
                      ? 'bg-primary/10 dark:bg-cyan-500/10 ring-1 ring-primary/30 dark:ring-cyan-500/30'
                      : 'hover:bg-gray-50 dark:hover:bg-white/5'
                  }`}
                >
                  <span
                    className={`material-symbols-outlined text-lg ${
                      selected.has(m.meetingId) ? 'text-primary dark:text-cyan-400' : 'text-gray-300 dark:text-gray-600'
                    }`}
                  >
                    {selected.has(m.meetingId) ? 'check_circle' : 'radio_button_unchecked'}
                  </span>
                  <div className="flex-1 min-w-0">
                    <p className="text-sm font-medium text-gray-900 dark:text-white truncate">{m.title}</p>
                    <p className="text-xs text-gray-400">
                      {new Date(m.date).toLocaleDateString('ko-KR', { month: 'short', day: 'numeric' })}
                    </p>
                  </div>
                </button>
              ))}
            </div>
          )}
        </div>

        {/* Footer */}
        <div className="px-5 py-4 border-t border-gray-200 dark:border-white/10">
          <button
            onClick={handleMerge}
            disabled={selected.size === 0 || merging}
            className="w-full py-2.5 rounded-lg text-sm font-semibold text-white bg-primary dark:bg-cyan-500 disabled:opacity-40 disabled:cursor-not-allowed hover:opacity-90 transition-opacity"
          >
            {merging ? '합치는 중...' : `${selected.size}개 미팅 합치기`}
          </button>
        </div>
      </div>
    </>
  );
}
```

- [ ] **Step 2: Verify frontend compiles**

Run: `cd /home/ec2-user/ttobak/frontend && npm run lint`
Expected: No new errors

- [ ] **Step 3: Commit**

```bash
git add frontend/src/components/meeting/MergeMeetingModal.tsx
git commit -m "feat(ui): add MergeMeetingModal for selecting meetings to merge"
```

---

## Task 10: Integrate Source Tabs & Merge into Meeting Detail Page

**Files:**
- Modify: `frontend/src/app/meeting/[id]/MeetingDetailClient.tsx`

- [ ] **Step 1: Add imports and state for source tabs**

At the top of `MeetingDetailClient.tsx`, add imports:

```tsx
import { AudioSourceTabs } from '@/components/meeting/AudioSourceTabs';
import { MergeMeetingModal } from '@/components/meeting/MergeMeetingModal';
import type { AudioSource } from '@/types/meeting';
```

Inside the main component function, add state:

```tsx
const [activeSourceId, setActiveSourceId] = useState<string>('');
const [showMergeModal, setShowMergeModal] = useState(false);
```

Add a derived value for the active source:

```tsx
const audioSources = meeting?.audioSources ?? [];
const activeSource = audioSources.find((s: AudioSource) => s.id === activeSourceId) ?? audioSources[0];

// Set initial active source when meeting loads
useEffect(() => {
  if (audioSources.length > 0 && !activeSourceId) {
    setActiveSourceId(audioSources[0].id);
  }
}, [audioSources, activeSourceId]);
```

- [ ] **Step 2: Add merge handler**

```tsx
const handleMerge = async (sourceMeetingIds: string[]) => {
  if (!meeting) return;
  await meetingsApi.merge(meeting.meetingId, sourceMeetingIds);
  // Reload meeting data
  const updated = await meetingsApi.get(meeting.meetingId);
  setMeeting(updated);
  setActiveSourceId('');
};
```

- [ ] **Step 3: Insert AudioSourceTabs before transcript/audio section**

Find the section where `SpeakerMapEditor`, `TranscriptSection`, and `AudioPlayer` are rendered. Wrap them with source tabs. The source-specific content (audio, speaker map, transcript) should read from `activeSource` instead of directly from `meeting`:

```tsx
{/* Source tabs — only visible when multiple sources exist */}
<AudioSourceTabs
  sources={audioSources}
  activeSourceId={activeSource?.id ?? ''}
  onSelectSource={setActiveSourceId}
  onMergeClick={() => setShowMergeModal(true)}
/>

{/* Source-specific content */}
{activeSource && (
  <>
    {activeSource.speakerMap && Object.keys(activeSource.speakerMap).length > 0 && (
      <SpeakerMapEditor
        meetingId={meeting.meetingId}
        speakerMap={activeSource.speakerMap}
        sourceId={activeSource.id}
      />
    )}
    {activeSource.transcription && activeSource.transcription.length > 0 && (
      <TranscriptSection transcription={activeSource.transcription} />
    )}
  </>
)}
```

For the AudioPlayer, use `activeSource.audioKey` to get the audio URL for the selected source.

- [ ] **Step 4: Add MergeMeetingModal at the end of the component**

```tsx
<MergeMeetingModal
  isOpen={showMergeModal}
  onClose={() => setShowMergeModal(false)}
  currentMeetingId={meeting?.meetingId ?? ''}
  onMerge={handleMerge}
/>
```

- [ ] **Step 5: Verify frontend compiles and runs**

Run: `cd /home/ec2-user/ttobak/frontend && npm run lint`
Expected: No new errors

Run: `cd /home/ec2-user/ttobak/frontend && npm run build`
Expected: Build succeeds

- [ ] **Step 6: Commit**

```bash
git add frontend/src/app/meeting/[id]/MeetingDetailClient.tsx
git commit -m "feat(ui): integrate AudioSourceTabs and merge modal into meeting detail"
```

---

## Task 11: Update Existing Audio & Transcript Endpoints for sourceId

**Files:**
- Modify: `backend/internal/handler/meeting.go` (GetAudioURL, SelectTranscript, UpdateSpeakers)

- [ ] **Step 1: Update GetAudioURL to support sourceId**

In the `GetAudioURL` handler, add sourceId support:

```go
func (h *MeetingHandler) GetAudioURL(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	meetingID := chi.URLParam(r, "meetingId")
	sourceID := r.URL.Query().Get("sourceId")

	// ... existing access check ...

	meeting.EnsureAudioSources()
	source := meeting.SelectedSource(sourceID)
	if source == nil || source.AudioKey == "" {
		writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "No audio found")
		return
	}

	// Generate presigned URL using source.AudioKey instead of meeting.AudioKey
	// ... rest of existing presigned URL logic using source.AudioKey ...
}
```

- [ ] **Step 2: Update SelectTranscript to support sourceId**

Add `sourceID` from query param. Update the specific AudioSource's SelectedTranscript instead of the meeting-level field.

- [ ] **Step 3: Verify Go compiles**

Run: `cd /home/ec2-user/ttobak/backend && /usr/local/go/bin/go build ./...`
Expected: Clean compile

- [ ] **Step 4: Commit**

```bash
git add backend/internal/handler/meeting.go
git commit -m "feat(api): add sourceId query param to audio and transcript endpoints"
```

---

## Task 12: Update Summarize Lambda for Multi-Source Trigger

**Files:**
- Modify: `backend/cmd/summarize/main.go`

- [ ] **Step 1: Update summarize Lambda handler**

The summarize Lambda is triggered by S3/EventBridge events. When triggered by the merge flow (meeting already has `status: summarizing` and multiple AudioSources), it should call `bedrockService.SummarizeTranscript()` which now handles multi-source automatically (from Task 6).

Verify the existing summarize Lambda handler already calls `bedrockService.SummarizeTranscript(meetingID)`. The multi-source logic was added in Task 6, so no changes may be needed here. However, the merge flow needs a way to trigger re-summarization.

Add a helper that can be called from the API Lambda to trigger summarization for the merged meeting. The simplest approach: the merge handler calls `bedrockService.SummarizeTranscript()` directly (synchronously) since the API Lambda already has Bedrock access via `summarizeLiveHandler`.

In `backend/cmd/api/main.go`, pass `bedrockRuntimeClient2` to the merge handler so it can trigger summarization:

```go
// Update MergeHandler to accept a BedrockService
mergeService := service.NewMergeService(repo, meetingService)
mergeHandler := handler.NewMergeHandler(mergeService, meetingService, bedrockServiceForAPI)
```

Where `bedrockServiceForAPI` is a `BedrockService` initialized in the API Lambda's `init()`.

- [ ] **Step 2: Add async summarization trigger in merge handler**

In `merge.go` handler, after the merge completes, trigger summarization in a goroutine (fire-and-forget — the status is already `summarizing`, and the frontend will poll):

```go
// After writeJSON response
go func() {
	ctx := context.Background()
	if _, err := h.bedrockService.SummarizeTranscript(ctx, merged.MeetingID, userID); err != nil {
		log.Printf("merge re-summarization failed for %s: %v", merged.MeetingID, err)
	}
}()
```

- [ ] **Step 3: Verify Go compiles**

Run: `cd /home/ec2-user/ttobak/backend && /usr/local/go/bin/go build ./...`
Expected: Clean compile

- [ ] **Step 4: Commit**

```bash
git add backend/cmd/api/main.go backend/internal/handler/merge.go backend/cmd/summarize/main.go
git commit -m "feat(summarize): trigger multi-source re-summarization after merge"
```

---

## Task 13: Build, Lint, and End-to-End Verification

**Files:** None (verification only)

- [ ] **Step 1: Build backend**

Run: `cd /home/ec2-user/ttobak/backend && /usr/local/go/bin/go build ./...`
Expected: Clean compile

- [ ] **Step 2: Run backend tests**

Run: `cd /home/ec2-user/ttobak/backend && /usr/local/go/bin/go test ./...`
Expected: All tests pass

- [ ] **Step 3: Lint frontend**

Run: `cd /home/ec2-user/ttobak/frontend && npm run lint`
Expected: No errors

- [ ] **Step 4: Build frontend**

Run: `cd /home/ec2-user/ttobak/frontend && npm run build`
Expected: Build succeeds

- [ ] **Step 5: Update API-SPEC.md**

Add the new `POST /api/meetings/{meetingId}/merge` endpoint documentation to `docs/API-SPEC.md`.

- [ ] **Step 6: Final commit**

```bash
git add docs/API-SPEC.md
git commit -m "docs: add merge endpoint to API spec"
```
