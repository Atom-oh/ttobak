package service

import (
	"errors"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func TestSidecarPDFKey(t *testing.T) {
	cases := []struct {
		name    string
		fileKey string
		want    string
	}{
		{"pptx under docs/", "docs/user-1/123_deck.pptx", "docs-pdf/user-1/123_deck.pptx.pdf"},
		{"ppt under docs/", "docs/user-1/123_deck.ppt", "docs-pdf/user-1/123_deck.ppt.pdf"},
		{"uppercase extension", "docs/user-1/123_DECK.PPTX", "docs-pdf/user-1/123_DECK.PPTX.pdf"},
		{"pdf needs no conversion", "docs/user-1/123_deck.pdf", ""},
		{"not under docs/ prefix", "audio/user-1/meeting-1/part.pptx", ""},
		{"unrelated category", "images/user-1/meeting-1/photo.jpg", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SidecarPDFKey(c.fileKey); got != c.want {
				t.Errorf("SidecarPDFKey(%q) = %q, want %q", c.fileKey, got, c.want)
			}
		})
	}
}

func TestValidateRediarizeEligibility(t *testing.T) {
	baseMeeting := func() *model.Meeting {
		return &model.Meeting{
			UserID:      "user-1",
			MeetingID:   "meeting-1",
			SttProvider: "whisper",
			Status:      model.StatusDone,
			AudioKey:    "audio/user-1/meeting-1/rec.webm",
		}
	}

	cases := []struct {
		name         string
		meeting      *model.Meeting
		userID       string
		speakerCount int
		wantKey      string
		wantErr      error // checked via errors.Is; nil means "any non-nil error"
		wantOK       bool
	}{
		{
			name:         "eligible whisper single-part done meeting",
			meeting:      baseMeeting(),
			userID:       "user-1",
			speakerCount: 6,
			wantKey:      "audio/user-1/meeting-1/rec.webm",
			wantOK:       true,
		},
		{name: "speakerCount too low", meeting: baseMeeting(), userID: "user-1", speakerCount: 0, wantErr: ErrInvalidInput},
		{name: "speakerCount too high", meeting: baseMeeting(), userID: "user-1", speakerCount: 21, wantErr: ErrInvalidInput},
		{name: "meeting not found", meeting: nil, userID: "user-1", speakerCount: 4, wantErr: ErrNotFound},
		{
			name: "non-whisper provider rejected",
			meeting: func() *model.Meeting {
				m := baseMeeting()
				m.SttProvider = "transcribe"
				return m
			}(),
			userID: "user-1", speakerCount: 4, wantErr: ErrInvalidInput,
		},
		{
			name: "multi-part via AudioPartCount rejected",
			meeting: func() *model.Meeting {
				m := baseMeeting()
				m.AudioPartCount = 2
				return m
			}(),
			userID: "user-1", speakerCount: 4, wantErr: ErrInvalidInput,
		},
		{
			name: "multi-part via AudioKeys length rejected even if AudioPartCount unset",
			meeting: func() *model.Meeting {
				m := baseMeeting()
				m.AudioKey = ""
				m.AudioKeys = []string{"audio/user-1/meeting-1/part_000_a.webm", "audio/user-1/meeting-1/part_001_a.webm"}
				return m
			}(),
			userID: "user-1", speakerCount: 4, wantErr: ErrInvalidInput,
		},
		{
			name: "currently transcribing rejected",
			meeting: func() *model.Meeting {
				m := baseMeeting()
				m.Status = model.StatusTranscribing
				return m
			}(),
			userID: "user-1", speakerCount: 4, wantErr: ErrInvalidInput,
		},
		{
			name: "currently summarizing rejected",
			meeting: func() *model.Meeting {
				m := baseMeeting()
				m.Status = model.StatusSummarizing
				return m
			}(),
			userID: "user-1", speakerCount: 4, wantErr: ErrInvalidInput,
		},
		{
			name: "no audio available rejected",
			meeting: func() *model.Meeting {
				m := baseMeeting()
				m.AudioKey = ""
				return m
			}(),
			userID: "user-1", speakerCount: 4, wantErr: ErrInvalidInput,
		},
		{
			name: "cross-user audio key rejected as forbidden",
			meeting: func() *model.Meeting {
				m := baseMeeting()
				m.AudioKey = "audio/other-user/meeting-1/rec.webm"
				return m
			}(),
			userID: "user-1", speakerCount: 4, wantErr: ErrForbidden,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			key, err := validateRediarizeEligibility(c.meeting, c.userID, c.speakerCount)
			if c.wantOK {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if key != c.wantKey {
					t.Errorf("got key %q, want %q", key, c.wantKey)
				}
				return
			}
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if c.wantErr != nil && !errors.Is(err, c.wantErr) {
				t.Errorf("got error %v, want it to match %v", err, c.wantErr)
			}
		})
	}
}
