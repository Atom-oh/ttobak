package repository

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
)

func TestInsightRevisionFollowsCapturedGenerationAcrossWriterRollback(t *testing.T) {
	for _, flag := range []string{"", "0", "1"} {
		for _, generation := range []string{"", "stored-generation"} {
			t.Run(flag+"/"+generation, func(t *testing.T) {
				t.Setenv("SUMMARY_PRIVACY_WRITER_ENABLED", flag)
				parent := privacyParent()
				parent.Content, parent.AuthorizationRevision = "summary", 7
				// Remove legacy revision: copying it makes generation-zero rows unreadable.
				parent.InsightsRevision = "previous-insight"
				if generation != "" {
					privacyStamp(parent, model.SummaryPrivacySafe, generation)
				}
				writes := 0
				repo := privacyWriterWire(t, privacyParentRows(parent),
					func(target string, input map[string]any) (int, string) {
						writes++
						if !strings.HasSuffix(target, ".UpdateItem") {
							t.Fatal("insight identity must publish atomically with its source-fenced result")
						}
						condition := resolved(input, "ConditionExpression")
						privacyWriterFences(t, condition, "insight publication lost %s source fence", []string{"content", "summaryPrivacy", "authorizationRevision", "insightsRevision", "previous-insight"})
						if generation != "" && !strings.Contains(condition, generation) {
							t.Fatal("rollback lost the captured stamped generation")
						}
						names := input["ExpressionAttributeNames"].(map[string]any)
						update := input["UpdateExpression"].(string)
						for alias, field := range names {
							if field != "insightsRevision" {
								continue
							}
							match := regexp.MustCompile(regexp.QuoteMeta(alias) + `\s*=\s*(:\w+)`).FindStringSubmatch(update)
							remove := regexp.MustCompile(`REMOVE\s+` + regexp.QuoteMeta(alias) + `(?:\s|,|$)`).MatchString(update)
							if generation == "" {
								if len(match) != 0 || !remove {
									t.Fatal("generation-zero publication retained an incompatible nonempty insight revision")
								}
							} else {
								if len(match) != 2 || remove {
									t.Fatal("stamped publication lost its current insight revision after writer rollback")
								}
								value := input["ExpressionAttributeValues"].(map[string]any)[match[1]].(map[string]any)["S"].(string)
								if _, err := uuid.Parse(value); err != nil || value == parent.InsightsRevision {
									t.Fatal("stamped publication did not install a fresh insight revision")
								}
							}
						}
						return 200, `{}`
					})
				snapshot, err := repo.CaptureSummaryPrivacy(context.Background(), "owner", "meeting")
				if err != nil {
					t.Fatal(err)
				}
				fields := map[string]interface{}{"insights": "fresh insight"}
				if err := repo.PublishSummaryDerivatives(context.Background(), snapshot, fields); err != nil {
					t.Fatal(err)
				}
				if writes != 1 || len(fields) != 1 || parent.InsightsRevision != "previous-insight" ||
					snapshot.Meeting.InsightsRevision != "previous-insight" {
					t.Fatal("publication replayed or mutated its source snapshot/caller fields")
				}
			})
		}
	}
}

func TestLegacyAndStampedInsightIdentityRetainConditionalWriteOutcomes(t *testing.T) {
	t.Setenv("SUMMARY_PRIVACY_WRITER_ENABLED", "0")
	for _, generation := range []string{"", "stored-generation"} {
		for _, rejection := range []string{"generation", "authorization", "insight revision", "ambiguous"} {
			t.Run(generation+"/"+rejection, func(t *testing.T) {
				parent := privacyParent()
				parent.Content, parent.AuthorizationRevision, parent.InsightsRevision = "summary", 7, "observed-insight"
				if generation != "" {
					privacyStamp(parent, model.SummaryPrivacySafe, generation)
				}
				writes := 0
				repo := privacyWriterWire(t, privacyParentRows(parent),
					func(_ string, body map[string]any) (int, string) {
						writes++
						condition := resolved(body, "ConditionExpression")
						field := map[string]string{"generation": "summaryPrivacy", "authorization": "authorizationRevision", "insight revision": "insightsRevision"}[rejection]
						if rejection != "ambiguous" && !strings.Contains(condition, field) {
							t.Fatal("changed source identity would not reject this write")
						}
						if rejection == "ambiguous" {
							return 500, `{"__type":"InternalServerError","message":"synthetic lost reply"}`
						}
						return 400, `{"__type":"ConditionalCheckFailedException"}`
					})
				snapshot, err := repo.CaptureSummaryPrivacy(context.Background(), "owner", "meeting")
				if err != nil {
					t.Fatal(err)
				}
				err = repo.PublishSummaryDerivatives(context.Background(), snapshot, map[string]interface{}{"insights": "late output"})
				if err == nil || errors.Is(err, ErrConditionFailed) != (rejection != "ambiguous") || writes != 1 {
					t.Fatalf("lost conditional/ambiguous outcome or replayed publication: %v writes=%d", err, writes)
				}
			})
		}
	}
}
