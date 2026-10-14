package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

func summaryTestService(client noteSourceHTTPClient) *BedrockService {
	cfg := aws.Config{Region: "ap-northeast-2", HTTPClient: client, Retryer: func() aws.Retryer { return aws.NopRetryer{} },
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "fixture", SecretAccessKey: "fixture"}, nil
		})}
	storage := s3.NewFromConfig(cfg)
	return NewBedrockService(bedrockruntime.NewFromConfig(cfg), storage, repository.NewDynamoDBRepositoryWithS3(dynamodb.NewFromConfig(cfg), "table", storage, "bucket"))
}

func TestUntimedSpeechRemainsEvidenceWithoutTimeAnchors(t *testing.T) {
	raw := `[{"id":"seg-10000","speaker":"spk_0","text":"timed","startTime":10,"endTime":11},{"id":"untimed-1","speaker":"spk_0","text":"preserved","startTime":0,"endTime":0,"untimed":true}]`
	m := &model.Meeting{TranscriptA: "timed preserved", TranscriptSegments: raw}
	segments := meetingTranscriptSegments(m, m.TranscriptA)
	if len(segments) != 2 || !segments[1].Untimed {
		t.Fatal("untimed speech lost")
	}
	prompt := buildSummarizeUserPrompt(m.TranscriptA, "", segments)
	if !strings.Contains(prompt, "시간 미확인") || !strings.Contains(prompt, "preserved") || strings.Contains(prompt, "0초~0초") {
		t.Fatal("summary lost raw speech or fabricated time")
	}
	windows := readingWindows(m.TranscriptA, raw)
	if len(windows) != 2 {
		t.Fatal("one untimed entry discarded valid windows")
	}
	chunk := readingChunkFor([]rune(m.TranscriptA), windows[1], windows[1].start, windows[1].end-windows[1].start)
	if chunk.Segment["timingScope"] != "unavailable" || chunk.Segment["startTime"] != nil {
		t.Fatal("untimed speech gained zero timestamp")
	}
	anchors := resolveTranscriptAnchors("[TS:0]", segments)
	if strings.Contains(anchors, "untimed-1") {
		t.Fatal("untimed speech became acoustic anchor")
	}
	renamed := renameSegmentSpeakers(raw, map[string]string{"spk_0": "Alice"})
	if !strings.Contains(renamed, `"untimed":true`) {
		t.Fatal("speaker edit discarded unknown timing")
	}
	export := GenerateMeetingDocument(m, nil)
	if !strings.Contains(export, "[time unavailable]: preserved") || strings.Contains(export, "[00:00]") {
		t.Fatal("KB export fabricated an unknown time")
	}
	m.TranscriptB = m.TranscriptA
	m.SelectedTranscript = "B"
	m.TranscriptQualityB = &model.TranscriptQuality{Refinement: "not_requested", Timing: "unavailable", TextSHA256: model.TranscriptHash(m.TranscriptB)}
	if len(meetingTranscriptSegments(m, m.TranscriptB)) != 0 {
		t.Fatal("B used sibling's equal-text timeline")
	}
	if strings.Contains(GenerateMeetingDocument(m, nil), "[00:10]") {
		t.Fatal("KB borrowed sibling timing")
	}
}

func TestMeetingDetailKeepsLegacyRawSpeechAndVersionedSegments(t *testing.T) {
	same := []speakerSegment{
		{ID: "seg-10000", Speaker: "spk_0", Text: "첫 발언", StartTime: 10, EndTime: 11},
		{ID: "untimed-1", Speaker: "spk_0", Text: "시간 모름 <script>&", Untimed: true},
		{ID: "seg-20000", Speaker: "spk_0", Text: "후속 발언", StartTime: 20, EndTime: 21},
	}
	for _, name := range []string{"mixed-same", "mixed-different", "all-untimed", "timed",
		"selected-B", "B-only", "equal-B-untimed", "stale-A"} {
		t.Run(name, func(t *testing.T) {
			segments := append([]speakerSegment(nil), same...)
			canonical := "[spk_0]\n첫 발언 시간 모름 <script>& 후속 발언"
			if name == "mixed-different" {
				segments[1].Speaker, segments[2].Speaker = "spk_1", "spk_2"
				canonical = "[spk_0]\n첫 발언\n\n[spk_1]\n시간 모름 <script>&\n\n[spk_2]\n후속 발언"
			}
			if name == "all-untimed" {
				for i := range segments {
					segments[i].StartTime, segments[i].EndTime, segments[i].Untimed = 0, 0, true
				}
			}
			if name == "timed" {
				segments[1].StartTime, segments[1].EndTime, segments[1].Untimed = 12, 13, false
			}
			raw, err := json.Marshal(segments)
			if err != nil {
				t.Fatal(err)
			}
			m := &model.Meeting{MeetingID: "m", UserID: "owner", Status: model.StatusDone,
				TranscriptA: canonical, TranscriptB: "other B text", TranscriptSegments: string(raw),
				Date: time.Now(), CreatedAt: time.Now(), UpdatedAt: time.Now()}
			if name == "selected-B" || name == "B-only" || name == "equal-B-untimed" {
				m.TranscriptB, m.SelectedTranscript = canonical, "B"
				if name == "B-only" {
					m.TranscriptA = ""
				}
				if name == "equal-B-untimed" {
					m.TranscriptQualityB = &model.TranscriptQuality{Refinement: "raw", Timing: "unavailable",
						TextSHA256: model.TranscriptHash(canonical), SegmentsSHA256: model.TranscriptHash(string(raw))}
				}
			}
			if name == "stale-A" {
				m.TranscriptA = "new human correction"
			}
			beforeA, beforeB := m.TranscriptA, m.TranscriptB
			repo := newMockMeetingRepo()
			repo.addMeeting(m)
			svc := newMeetingServiceWithRepo(repo)
			detail, err := svc.GetMeetingDetail(context.Background(), "owner", "m")
			if err != nil {
				t.Fatal(err)
			}
			if detail.TranscriptA != beforeA || detail.TranscriptB != beforeB ||
				m.TranscriptA != beforeA || m.TranscriptB != beforeB {
				t.Fatal("presentation changed canonical source bytes")
			}
			body, err := json.Marshal(detail)
			if err != nil {
				t.Fatal(err)
			}
			var legacy struct {
				TranscriptA, TranscriptB string
				Transcription            []speakerSegment
			}
			if err := json.Unmarshal(body, &legacy); err != nil {
				t.Fatal(err)
			}
			if name == "timed" {
				if len(legacy.Transcription) != len(segments) || len(detail.TranscriptionV2) != 0 {
					t.Fatal("wholly timed legacy response changed or duplicated")
				}
			} else if name == "equal-B-untimed" || name == "stale-A" {
				if len(legacy.Transcription) != 0 || len(detail.TranscriptionV2) != 0 {
					t.Fatal("stale/sibling timing was reused")
				}
			} else {
				if len(legacy.Transcription) != 0 {
					t.Fatal("legacy client would hide raw speech or invent a zero timestamp")
				}
				var current []speakerSegment
				if json.Unmarshal(detail.TranscriptionV2, &current) != nil || len(current) != len(segments) {
					t.Fatal("versioned response dropped speech")
				}
				var fields []map[string]interface{}
				if json.Unmarshal(detail.TranscriptionV2, &fields) != nil {
					t.Fatal("invalid versioned segment JSON")
				}
				for _, segment := range fields {
					if segment["untimed"] == true && (segment["startTime"] != nil || segment["endTime"] != nil) {
						t.Fatal("untimed speech leaked zero timestamps")
					}
				}
				for i, segment := range current {
					if segment != segments[i] {
						t.Fatal("versioned response changed speech, labels or timing")
					}
				}
			}
			if name == "selected-B" || name == "B-only" || name == "equal-B-untimed" {
				if detail.SelectedTranscript == nil || *detail.SelectedTranscript != "B" || legacy.TranscriptB != canonical {
					t.Fatal("legacy fallback lost the selected B source")
				}
			}
		})
	}
}

func TestLegacyTranscriptTextOnlyUpdateRemainsSupported(t *testing.T) {
	repo := newMockMeetingRepo()
	repo.addMeeting(&model.Meeting{MeetingID: "m", UserID: "owner", Status: model.StatusDone,
		TranscriptA: "old source", Date: time.Now(), CreatedAt: time.Now(), UpdatedAt: time.Now()})
	var request model.UpdateMeetingRequest
	if err := json.Unmarshal([]byte(`{"transcriptA":"human correction <script>&"}`), &request); err != nil {
		t.Fatal(err)
	}
	if _, err := newMeetingServiceWithRepo(repo).UpdateMeeting(context.Background(), "owner", "m", &request); err != nil {
		t.Fatalf("legacy text-only edit rejected: %v", err)
	}
	if repo.meetings[meetingKey("owner", "m")].TranscriptA != request.TranscriptA {
		t.Fatal("legacy edit projected or lost canonical text")
	}
}

func TestSummarySegmentReadFailuresNeverBecomeSuccessfulAbsence(t *testing.T) {
	for _, test := range [][2]int{{500, 0}, {412, 0}, {500, 2}, {500, 3}} {
		code, failHead := test[0], test[1]
		models, saves, conflicts, heads := 0, 0, 0, 0
		client := noteSourceHTTPClient(func(req *http.Request) (*http.Response, error) {
			status, body := 200, `{}`
			switch req.Header.Get("X-Amz-Target") {
			case "DynamoDB_20120810.GetItem":
				body = `{"Item":{"PK":{"S":"USER#owner"},"SK":{"S":"MEETING#m"},"userId":{"S":"owner"},"meetingId":{"S":"m"},"status":{"S":"summarizing"},"transcriptA":{"S":"실제 발언"},"transcriptSegments":{"S":"s3://bucket/transcripts/m/transcriptSegments.txt"}}}`
			case "DynamoDB_20120810.UpdateItem":
				conflicts++
			case "DynamoDB_20120810.TransactWriteItems":
				saves++
			case "DynamoDB_20120810.Query":
				body = `{"Items":[]}`
			default:
				if req.Method == "HEAD" {
					heads++
					if heads == failHead {
						return indexHTTPResponse(code, "", nil), nil
					}
					return indexHTTPResponse(200, "", map[string]string{"ETag": `"v1"`, "Content-Length": "6"}), nil
				}
				if req.Method == "GET" {
					if failHead > 0 {
						return indexHTTPResponse(200, "한글", map[string]string{"ETag": `"v1"`}), nil
					}
					status = code
					body = `<Error><Code>InternalError</Code></Error>`
					if code == 412 {
						body = `<Error><Code>PreconditionFailed</Code></Error>`
					}
				} else {
					models++
					body = `{"content":[{"type":"text","text":"조용히 시각을 잃은 요약"}],"stop_reason":"end_turn"}`
				}
			}
			return indexHTTPResponse(status, body, nil), nil
		})
		svc := summaryTestService(client)
		_, err := svc.SummarizeTranscript(context.Background(), "m", "owner", "")
		wantModels := 0
		if failHead == 3 {
			wantModels = 1
		}
		if err == nil || models != wantModels || saves != 0 || (code == 412 && (!errors.Is(err, ErrSummaryConflict) || conflicts != 1)) ||
			code == 500 && (errors.Is(err, ErrSummaryConflict) || conflicts != 0) {
			t.Errorf("code=%d head=%d model=%d save=%d conflict=%d err=%v", code, failHead, models, saves, conflicts, err)
		}
	}
}
