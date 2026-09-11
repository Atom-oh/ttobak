package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

const (
	policyCurrentText = "SELECTED_SOURCE: budget 1.5, not 15."
	policyOtherText   = "OTHER_SOURCE: budget 90."
	policySegments    = `[{"id":"match-12","speaker":"Kim","text":"SELECTED_SOURCE: budget 1.5, not 15.","startTime":12,"endTime":18}]`
)

// Check real consumer requests/output, not just a selector that a caller could
// forget to use. All transport boundaries are the in-memory fixture.
func assertTranscriptConsumers(t *testing.T, meeting *model.Meeting, wantText string, wantSegments bool) {
	t.Helper()
	for _, consumer := range []struct {
		name, modelText string
		invoke          func(*BedrockService) (string, error)
		hasAnchors      bool
	}{
		{
			name: "summary", modelText: "answer [TS:12]", hasAnchors: true,
			invoke: func(s *BedrockService) (string, error) {
				return s.SummarizeTranscript(context.Background(), meeting.MeetingID, meeting.UserID, "")
			},
		},
		{
			name: "simulation", modelText: `[{"key":"monthlyActiveUsers","value":"100000","required":true,"tsMarker":"[TS:12]"}]`, hasAnchors: true,
			invoke: func(s *BedrockService) (string, error) {
				result, err := s.ExtractSimRequirements(context.Background(), meeting)
				if err != nil {
					return "", err
				}
				if len(result) != 1 {
					t.Fatalf("simulation response unexpectedly dropped: %+v", result)
				}
				return result[0].Evidence, nil
			},
		},
		{
			name: "tags", modelText: `["scope"]`,
			invoke: func(s *BedrockService) (string, error) {
				result, err := s.ExtractTags(context.Background(), meeting.MeetingID, meeting.UserID)
				return strings.Join(result, ","), err
			},
		},
		{
			name: "sentiment fallback", modelText: "neutral",
			invoke: func(s *BedrockService) (string, error) {
				return s.ExtractSentiment(context.Background(), meeting.MeetingID, meeting.UserID)
			},
		},
	} {
		t.Run(consumer.name, func(t *testing.T) {
			response, err := json.Marshal(map[string]interface{}{
				"content":     []map[string]string{{"type": "text", "text": consumer.modelText}},
				"stop_reason": "end_turn",
			})
			if err != nil {
				t.Fatal(err)
			}
			request, result, _, err := invokeNoteSourceFixture(t, meeting, string(response), consumer.invoke)
			if err != nil {
				t.Fatal(err)
			}
			if len(request.Messages) == 0 {
				t.Fatal("selected transcript never reached the model")
			}
			prompt := request.Messages[0].Content[0].Text
			if !strings.Contains(prompt, wantText) {
				t.Errorf("selected text missing: %q", prompt)
			}
			unwanted := policyOtherText
			if wantText == policyOtherText {
				unwanted = policyCurrentText
			}
			if strings.Contains(prompt, unwanted) {
				t.Errorf("other/stale source reached the model: %q", prompt)
			}
			if strings.Contains(prompt, "Kim") != wantSegments {
				t.Errorf("verified segment usage = %v, want %v: %q", strings.Contains(prompt, "Kim"), wantSegments, prompt)
			}
			if consumer.hasAnchors && strings.Contains(result, "transcript://match-12") != wantSegments {
				t.Errorf("anchor usage does not match verified source: %q", result)
			}
		})
	}
	t.Run("detail", func(t *testing.T) {
		repo := newMockMeetingRepo()
		repo.addMeeting(meeting)
		detail, err := newMeetingServiceWithRepo(repo).GetMeetingDetail(context.Background(), meeting.UserID, meeting.MeetingID)
		if err != nil {
			t.Fatal(err)
		}
		var segments []speakerSegment
		if len(detail.Transcription) > 0 {
			if err := json.Unmarshal(detail.Transcription, &segments); err != nil {
				t.Fatal(err)
			}
		}
		if wantSegments {
			if len(segments) != 1 || segments[0].ID != "match-12" || segments[0].Text != wantText {
				t.Errorf("detail lost verified selected-source segments: %+v", segments)
			}
		} else if len(segments) != 0 {
			t.Errorf("detail rendered unverified metadata: %+v", segments)
		}
	})
	t.Run("KB and vault export", func(t *testing.T) {
		document := GenerateMeetingDocument(meeting, nil)
		if !strings.Contains(document, wantText) {
			t.Errorf("export lost selected transcript: %q", document)
		}
		unwanted := policyOtherText
		if wantText == policyOtherText {
			unwanted = policyCurrentText
		}
		if strings.Contains(document, unwanted) || strings.Contains(document, "**Kim**") != wantSegments {
			t.Errorf("export used the wrong candidate segments: %q", document)
		}
	})
}

func TestTranscriptSourcePolicy_AllConsumers(t *testing.T) {
	for _, tt := range []struct {
		name, a, b, selected, segments, want, variant string
		wantSegments                                  bool
	}{
		{"B-only", "", policyCurrentText, "", policySegments, policyCurrentText, "B", true},
		{"default A", policyCurrentText, policyOtherText, "", policySegments, policyCurrentText, "A", true},
		{"null segments", policyCurrentText, policyOtherText, "A", "null", policyCurrentText, "A", false},
		{"empty segments", policyCurrentText, policyOtherText, "A", "[]", policyCurrentText, "A", false},
		{"preserve raw whitespace", "", "\n " + policyCurrentText + " \t", "B", "", "\n " + policyCurrentText + " \t", "B", false},
		{"legacy B punctuation", "", policyCurrentText, "B", `[{"id":"match-12","speaker":"Kim","text":"SELECTED_SOURCE budget 1.5 not 15","startTime":12,"endTime":18}]`, policyCurrentText, "B", true},
		{"explicit B", policyOtherText, policyCurrentText, "B", policySegments, policyCurrentText, "B", true},
		{"equal variants B", policyCurrentText, policyCurrentText, "B", policySegments, policyCurrentText, "B", true},
		{"equal variants A", policyCurrentText, policyCurrentText, "A", policySegments, policyCurrentText, "A", true},
		{"selected B rejects A segments", policyCurrentText, policyOtherText, "B", policySegments, policyOtherText, "B", false},
		{"selected A rejects B segments", policyOtherText, policyCurrentText, "A", policySegments, policyOtherText, "A", false},
		{"whitespace A falls back to B", " \n\t", policyCurrentText, "", policySegments, policyCurrentText, "B", true},
		{"whitespace B falls back to A", policyCurrentText, " \n\t", "B", policySegments, policyCurrentText, "A", true},
		{"B raw fallback", "", policyCurrentText, "", "", policyCurrentText, "B", false},
		{"B malformed segments fallback", "", policyCurrentText, "B", `[{"text":7}]`, policyCurrentText, "B", false},
		{"A malformed segments fallback", policyCurrentText, policyOtherText, "A", `[{"text":7}]`, policyCurrentText, "A", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			meeting := &model.Meeting{
				MeetingID: "m-1", UserID: "owner-1", Status: model.StatusDone,
				TranscriptA: tt.a, TranscriptB: tt.b, SelectedTranscript: tt.selected,
				TranscriptSegments: tt.segments,
			}
			assertTranscriptConsumers(t, meeting, tt.want, tt.wantSegments)
			repo := newMockMeetingRepo()
			repo.addMeeting(meeting)
			detail, err := newMeetingServiceWithRepo(repo).GetMeetingDetail(context.Background(), meeting.UserID, meeting.MeetingID)
			if err != nil {
				t.Fatal(err)
			}
			if detail.SelectedTranscript == nil || *detail.SelectedTranscript != tt.variant {
				t.Errorf("detail must identify the effective variant for exports: %+v", detail.SelectedTranscript)
			}
			if meeting.SelectedTranscript != tt.selected || detail.TranscriptA != tt.a || detail.TranscriptB != tt.b {
				t.Fatal("read-time selection rewrote the stored preference or raw text")
			}
		})
	}
}

func TestTranscriptSourcePolicy_NoAvailableTranscript(t *testing.T) {
	meeting := &model.Meeting{
		MeetingID: "m-1", UserID: "owner-1", Status: model.StatusDone,
		TranscriptA: "\t", TranscriptB: " ", SelectedTranscript: "B", TranscriptSegments: policySegments,
	}
	for _, consumer := range []struct {
		name    string
		invoke  func(*BedrockService) (string, error)
		wantErr bool
	}{
		{"summary", func(s *BedrockService) (string, error) {
			return s.SummarizeTranscript(context.Background(), "m-1", "owner-1", "")
		}, true},
		{"simulation", func(s *BedrockService) (string, error) {
			result, err := s.ExtractSimRequirements(context.Background(), meeting)
			if len(result) != 0 {
				t.Errorf("orphan metadata produced simulation requirements: %+v", result)
			}
			return "", err
		}, false},
		{"tags", func(s *BedrockService) (string, error) {
			result, err := s.ExtractTags(context.Background(), "m-1", "owner-1")
			return strings.Join(result, ","), err
		}, false},
		{"sentiment", func(s *BedrockService) (string, error) {
			return s.ExtractSentiment(context.Background(), "m-1", "owner-1")
		}, false},
	} {
		t.Run(consumer.name, func(t *testing.T) {
			request, result, writes, err := invokeNoteSourceFixture(t, meeting,
				`{"content":[{"type":"text","text":"orphan metadata must not be used"}],"stop_reason":"end_turn"}`, consumer.invoke)
			if (err != nil) != consumer.wantErr || result != "" || len(request.Messages) != 0 || writes != 0 {
				t.Errorf("empty source used orphan metadata: result=%q err=%v request=%+v writes=%d", result, err, request, writes)
			}
		})
	}
	repo := newMockMeetingRepo()
	repo.addMeeting(meeting)
	detail, err := newMeetingServiceWithRepo(repo).GetMeetingDetail(context.Background(), "owner-1", "m-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Transcription) != 0 || detail.SelectedTranscript != nil {
		t.Error("empty source exposed a selected variant or orphan segments")
	}
	if document := GenerateMeetingDocument(meeting, nil); strings.Contains(document, "## Transcript") || strings.Contains(document, policyCurrentText) {
		t.Errorf("export used orphan segments: %q", document)
	}
}

func TestExtractSimRequirements_ContentFallbackHasNoTranscriptAnchors(t *testing.T) {
	meeting := &model.Meeting{
		MeetingID: "m-1", UserID: "owner-1", Status: model.StatusDone,
		TranscriptA: "\t", TranscriptB: " ", Content: policyCurrentText, TranscriptSegments: policySegments,
	}
	request, evidence, _, err := invokeNoteSourceFixture(t, meeting,
		`{"content":[{"type":"text","text":"[{\"key\":\"monthlyActiveUsers\",\"value\":\"100000\",\"tsMarker\":\"[TS:12]\"}]"}],"stop_reason":"end_turn"}`,
		func(s *BedrockService) (string, error) {
			result, err := s.ExtractSimRequirements(context.Background(), meeting)
			if err != nil {
				return "", err
			}
			if len(result) != 1 {
				t.Fatalf("missing simulation result: %+v", result)
			}
			return result[0].Evidence, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	prompt := request.Messages[0].Content[0].Text
	if !strings.Contains(prompt, policyCurrentText) || strings.Contains(prompt, "Kim") || evidence != "" {
		t.Errorf("summary fallback borrowed transcript metadata: prompt=%q evidence=%q", prompt, evidence)
	}
}

func TestUpdateMeeting_AEditPreservesBMetadata(t *testing.T) {
	for _, concurrent := range []bool{false, true} {
		name := "existing B candidates"
		if concurrent {
			name = "B producer writes after the access read"
		}
		t.Run(name, func(t *testing.T) {
			repo := &noteSourceMeetingRepo{mockMeetingRepo: newMockMeetingRepo()}
			repo.addMeeting(&model.Meeting{
				MeetingID: "m-1", UserID: "owner-1", Status: model.StatusDone,
				TranscriptA: "original A", SelectedTranscript: "B",
			})
			produceB := func() {
				row := repo.meetingsByID["m-1"]
				row.TranscriptB, row.TranscriptSegments = policyCurrentText, policySegments
			}
			if concurrent {
				repo.beforeUpdate = produceB
			} else {
				produceB()
			}
			_, err := newMeetingServiceWithRepo(repo).UpdateMeeting(context.Background(), "owner-1", "m-1", &model.UpdateMeetingRequest{
				TranscriptA: policyOtherText,
			})
			if err != nil {
				t.Fatal(err)
			}
			if repo.wholeUpdates != 0 || len(repo.updates) != 1 {
				t.Fatal("A edits must remain partial writes")
			}
			if _, touched := repo.updates[0]["transcriptSegments"]; touched {
				t.Error("A edit must not overwrite metadata shared with B")
			}
			stored := repo.meetingsByID["m-1"]
			if stored.TranscriptA != policyOtherText || stored.TranscriptB != policyCurrentText || stored.TranscriptSegments != policySegments {
				t.Fatalf("A edit destroyed a B source: %+v", stored)
			}
			assertTranscriptConsumers(t, stored, policyCurrentText, true)
		})
	}
}

func TestUpdateMeeting_StaleACandidatesAreIgnoredByAllConsumers(t *testing.T) {
	repo := newMockMeetingRepo()
	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "owner-1", Status: model.StatusDone,
		TranscriptA: policyCurrentText, TranscriptSegments: policySegments,
	})
	_, err := newMeetingServiceWithRepo(repo).UpdateMeeting(context.Background(), "owner-1", "m-1", &model.UpdateMeetingRequest{
		TranscriptA: policyOtherText,
	})
	if err != nil {
		t.Fatal(err)
	}
	stored := repo.meetingsByID["m-1"]
	if stored.TranscriptSegments != policySegments {
		t.Fatal("candidate metadata must be preserved")
	}
	assertTranscriptConsumers(t, stored, policyOtherText, false)
}

func TestUpdateMeeting_WhitespaceTranscriptAIsRejected(t *testing.T) {
	for _, text := range []string{" ", "\n\t\r", "　"} {
		t.Run(text, func(t *testing.T) {
			repo := &noteSourceMeetingRepo{mockMeetingRepo: newMockMeetingRepo()}
			repo.addMeeting(&model.Meeting{
				MeetingID: "m-1", UserID: "owner-1", Status: model.StatusDone,
				Title: "original", TranscriptA: policyCurrentText, TranscriptSegments: policySegments,
			})
			_, err := newMeetingServiceWithRepo(repo).UpdateMeeting(context.Background(), "owner-1", "m-1", &model.UpdateMeetingRequest{
				Title: "changed", TranscriptA: text,
			})
			if !errors.Is(err, ErrInvalidInput) || len(repo.updates) != 0 || repo.wholeUpdates != 0 {
				t.Errorf("blank edit must reject the entire request: err=%v updates=%v", err, repo.updates)
			}
			stored := repo.meetingsByID["m-1"]
			if stored.Title != "original" || stored.TranscriptA != policyCurrentText || stored.TranscriptSegments != policySegments {
				t.Fatal("rejected blank edit changed source data")
			}
		})
	}
}
