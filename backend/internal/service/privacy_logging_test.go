package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func capturePrivacyLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	prior := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(prior) })
	return &output
}

type failingPublicShareCleanupRepo struct {
	*mockAccountRepo
	deletedTokens []string
}

func (r *failingPublicShareCleanupRepo) DeletePublicShare(_ context.Context, token string) error {
	r.deletedTokens = append(r.deletedTokens, token)
	return fmt.Errorf("SYNTHETIC_PROVIDER_ERROR token=%s", token)
}

func TestPublicShareCleanupLogsExcludeTokenAndProviderError(t *testing.T) {
	for _, operation := range []string{"mint", "revoke", "delete", "conversion"} {
		t.Run(operation, func(t *testing.T) {
			logs := capturePrivacyLogs(t)
			repo := &failingPublicShareCleanupRepo{mockAccountRepo: newMockAccountRepo()}
			svc := &AccountService{repo: repo, s3: &mockS3Deleter{}, bucketName: "test-bucket"}
			ctx := context.Background()
			doc, err := svc.PutUserDocument(ctx, "owner", &model.PutDocumentRequest{
				Title: "Synthetic document", FileKey: "docs/owner/fixture.pdf", FileName: "fixture.pdf",
			})
			if err != nil {
				t.Fatal(err)
			}
			if operation == "mint" {
				repo.raceWinnerToken = "SYNTHETIC_WINNER_CAPABILITY"
			}
			_, err = svc.CreateUserDocPublicShare(ctx, "owner", doc.DocID)
			if err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "revoke":
				err = svc.RevokeUserDocPublicShare(ctx, "owner", doc.DocID)
			case "delete":
				err = svc.DeleteUserDocument(ctx, "owner", doc.DocID)
			case "conversion":
				_, err = svc.UpdateUserDocument(ctx, "owner", doc.DocID, &model.PutDocumentRequest{
					Title: "Synthetic note", Markdown: mdPtr("Synthetic text"),
				})
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(repo.deletedTokens) != 1 {
				t.Fatalf("cleanup calls=%d, want 1", len(repo.deletedTokens))
			}
			output := logs.String()
			for _, secret := range append(repo.deletedTokens, "SYNTHETIC_PROVIDER_ERROR", "SYNTHETIC_WINNER_CAPABILITY") {
				if strings.Contains(output, secret) {
					t.Fatal("public-share cleanup logged a capability or provider text")
				}
			}
			if !strings.Contains(output, "public share cleanup failed operation="+operation) ||
				!strings.Contains(output, doc.DocID) {
				t.Fatal("cleanup failure lost its operation/document diagnostic")
			}
		})
	}
}

func TestRefinementFailuresNeverExposeSourceOrProviderText(t *testing.T) {
	const canary = "SYNTHETIC_PRIVATE_REFINEMENT_CANARY"
	for _, failure := range []string{"json", "provider", "empty", "speaker", "panic"} {
		for _, parallel := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/parallel=%v", failure, parallel), func(t *testing.T) {
				logs := capturePrivacyLogs(t)
				output := "[" + canary + "]"
				wantCode := "invalid_json"
				switch failure {
				case "provider":
					wantCode = "model_failure"
				case "empty":
					output, wantCode = "[]", "empty_result"
				case "speaker":
					output = `[{"speaker":"spk_0","text":"` + canary + `","startTime":0,"endTime":2}]`
					wantCode = "speaker_merge"
				case "panic":
					wantCode = "panic"
				}
				body, err := json.Marshal(map[string]any{
					"content": []map[string]string{{"type": "text", "text": output}}, "stop_reason": "end_turn",
				})
				if err != nil {
					t.Fatal(err)
				}
				svc := summaryTestService(noteSourceHTTPClient(func(*http.Request) (*http.Response, error) {
					if failure == "provider" {
						return nil, errors.New(canary)
					}
					if failure == "panic" {
						panic(canary)
					}
					return indexHTTPResponse(200, string(body), nil), nil
				}))
				input := []WhisperSegment{
					{Start: 0, End: 1, Speaker: "spk_0", Text: canary + "_first"},
					{Start: 1, End: 2, Speaker: "spk_1", Text: canary + "_second"},
				}
				chunks := [][]WhisperSegment{input, input, input, input, input, input}
				var result []RefinedSegment
				if parallel {
					result = svc.refineParallel(context.Background(), chunks)
				} else {
					result = svc.refineSequential(context.Background(), chunks)
				}
				if len(result) != len(chunks)*len(input) || result[0].Text != input[0].Text ||
					result[len(result)-1].Text != input[1].Text {
					t.Fatal("refinement failure did not retain every raw segment")
				}
				if strings.Contains(logs.String(), canary) {
					t.Fatal("refinement log exposed source or provider text")
				}
				if strings.Count(logs.String(), "code="+wantCode) != len(chunks) {
					t.Fatalf("missing failure categories: %s", logs.String())
				}
				// The error itself must also be safe for upstream callers to log.
				if failure == "json" || failure == "provider" || failure == "panic" {
					_, err := svc.refineChunk(context.Background(), input, 0, 1, nil)
					if err == nil || strings.Contains(err.Error(), canary) {
						t.Fatal("refinement returned an unsafe error")
					}
				}
			})
		}
	}
}

func TestRefinementPanicLogExcludesPanicValue(t *testing.T) {
	const canary = "SYNTHETIC_PRIVATE_PANIC_VALUE"
	logs := capturePrivacyLogs(t)
	input := []WhisperSegment{{Start: 0, End: 1, Text: canary, Speaker: "spk_0"}}
	done := make(chan chunkResult, 1)
	func() {
		defer recoverRefineChunk(2, 6, done, input, nil)
		panic(canary)
	}()
	result := <-done
	if result.index != 2 || len(result.segments) != 1 || result.segments[0].Text != canary {
		t.Fatal("panic fallback lost source content")
	}
	if strings.Contains(logs.String(), canary) || !strings.Contains(logs.String(), "code=panic") {
		t.Fatal("panic log exposed its value or lost the failure category")
	}
}

func TestRefinementUnknownErrorLogIsClosed(t *testing.T) {
	logs := capturePrivacyLogs(t)
	logRefinementFallback(0, 1, errors.New("SYNTHETIC_FUTURE_PROVIDER_ERROR"))
	if strings.Contains(logs.String(), "SYNTHETIC_FUTURE_PROVIDER_ERROR") ||
		!strings.Contains(logs.String(), "code=unknown") {
		t.Fatal("unrecognized refinement error was not redacted")
	}
}
