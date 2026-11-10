package repository

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

// englishTestSnapshot mirrors a CaptureResummary result: the canonical
// meeting check carries every captured field, followed by the action-analysis
// fence, an editor share grant, and an attachment plus its text state.
func englishTestSnapshot(korean string) *model.SummarySnapshot {
	fields := map[string]model.SummaryValue{}
	for _, name := range withSummaryPrivacyFields(withSummaryClearanceFields(summaryMeetingFields)) {
		fields[name] = model.SummaryValue{}
	}
	fields["meetingId"] = model.SummaryValue{Present: true, Value: "meeting"}
	fields["userId"] = model.SummaryValue{Present: true, Value: "owner"}
	fields["status"] = model.SummaryValue{Present: true, Value: model.StatusDone}
	fields["content"] = model.SummaryValue{Present: true, Value: korean}
	fields["actionItems"] = model.SummaryValue{Present: true, Value: "[]"}
	fields["transcriptA"] = model.SummaryValue{Present: true, Value: "saved source"}
	fields["selectedTranscript"] = model.SummaryValue{Present: true, Value: "A"}
	fields["transcriptRevisionA"] = model.SummaryValue{Present: true, Value: "rev-a"}
	return &model.SummarySnapshot{
		Meeting: &model.Meeting{UserID: "owner", MeetingID: "meeting", Content: korean},
		Stored:  map[string]interface{}{"content": korean, "transcriptA": "saved source", "meetingId": "meeting", "userId": "owner"},
		Checks: []model.SummaryCheck{
			{PK: "USER#owner", SK: "MEETING#meeting", Exists: true, Fields: fields},
			{PK: "MEETING#meeting", SK: model.ActionAnalysisSK, Exists: true, Fields: map[string]model.SummaryValue{"runId": {Present: true, Value: "tasks"}}},
			{PK: "USER#editor", SK: "SHARED#meeting", Exists: true, Fields: map[string]model.SummaryValue{"permission": {Present: true, Value: model.PermissionEdit}}},
			{PK: "MEETING#meeting", SK: "ATTACH#a", Exists: true, Fields: map[string]model.SummaryValue{"originalKey": {Present: true, Value: "files/owner/meeting/a.pdf"}}},
			{PK: "MEETING#meeting", SK: "ATTEXT#a", Exists: true, Fields: map[string]model.SummaryValue{"runId": {Present: true, Value: "extract"}}},
		},
	}
}

func englishTestState(now time.Time) *model.ResummaryState {
	return &model.ResummaryState{RunID: "en-run", Status: model.AnalysisRunning, OwnerID: "owner", RequestedBy: "editor",
		SourceHash: "source-hash", LeaseUntil: now.Add(time.Minute).UnixMilli()}
}

func opKey(op map[string]any) (string, string) {
	key := op["Key"].(map[string]any)
	return key["PK"].(map[string]any)["S"].(string), key["SK"].(map[string]any)["S"].(string)
}

// opNames returns every attribute name an operation references.
func opNames(op map[string]any) []string {
	var names []string
	if raw, ok := op["ExpressionAttributeNames"].(map[string]any); ok {
		for _, name := range raw {
			names = append(names, name.(string))
		}
	}
	sort.Strings(names)
	return names
}

// updatedNames returns the attribute names assigned by an Update's SET clause.
func updatedNames(t *testing.T, op map[string]any) []string {
	t.Helper()
	expr := op["UpdateExpression"].(string)
	aliases := op["ExpressionAttributeNames"].(map[string]any)
	set := strings.TrimSpace(strings.TrimPrefix(expr, "SET"))
	if strings.ContainsAny(set, "\n") || strings.Contains(set, "REMOVE") || strings.Contains(set, "ADD ") || strings.Contains(set, "DELETE ") {
		t.Fatalf("unexpected update clauses: %s", expr)
	}
	var names []string
	for _, assignment := range strings.Split(set, ",") {
		alias := strings.TrimSpace(strings.SplitN(assignment, "=", 2)[0])
		names = append(names, aliases[alias].(string))
	}
	sort.Strings(names)
	return names
}

func captureEnglishTransaction(t *testing.T) (*[]map[string]any, *DynamoDBRepository) {
	var ops []map[string]any
	repo := actionAnalysisWire(t, func(target string, body map[string]any) {
		if !strings.HasSuffix(target, ".TransactWriteItems") {
			t.Fatalf("English write is not one transaction: %s", target)
		}
		ops = nil
		for _, raw := range body["TransactItems"].([]any) {
			ops = append(ops, raw.(map[string]any))
		}
	}, 200, `{}`)
	return &ops, repo
}

func TestCompleteEnglishSummaryWritesOnlyEnglishFields(t *testing.T) {
	ops, repo := captureEnglishTransaction(t)
	now := time.Now().UTC()
	if err := repo.CompleteEnglishSummary(context.Background(), englishTestSnapshot("korean note"), englishTestState(now), "english note", "source-hash", now); err != nil {
		t.Fatal(err)
	}
	if len(*ops) < 2 {
		t.Fatalf("missing writes: %v", *ops)
	}
	meeting := (*ops)[0]["Update"].(map[string]any)
	if pk, sk := opKey(meeting); pk != "USER#owner" || sk != "MEETING#meeting" {
		t.Fatalf("first write is not the meeting: %s %s", pk, sk)
	}
	if got := strings.Join(updatedNames(t, meeting), ","); got != "contentEn,contentEnGeneratedAt,contentEnSourceHash" {
		t.Fatalf("English completion wrote %s", got)
	}
	run := (*ops)[1]["Update"].(map[string]any)
	if pk, sk := opKey(run); pk != "MEETING#meeting" || sk != model.EnglishSummarySK {
		t.Fatalf("state write targets %s %s", pk, sk)
	}
	for i, op := range (*ops)[2:] {
		if op["ConditionCheck"] == nil {
			t.Fatalf("item %d is not a read-only fence: %v", i+2, op)
		}
		check := op["ConditionCheck"].(map[string]any)
		if _, sk := opKey(check); sk == model.ActionAnalysisSK || sk == model.ResummarySK || sk == model.PrefixSimRun {
			t.Fatalf("English completion touches Korean derivative state %s", sk)
		}
	}
	values := (*ops)[0]["Update"].(map[string]any)["ExpressionAttributeValues"].(map[string]any)
	found := false
	for _, value := range values {
		if s, ok := value.(map[string]any)["S"]; ok && s == "english note" {
			found = true
		}
	}
	if !found {
		t.Fatal("English content missing from publication")
	}
}

func TestCompleteEnglishSummaryPinsSourceNotKoreanContent(t *testing.T) {
	ops, repo := captureEnglishTransaction(t)
	now := time.Now().UTC()
	if err := repo.CompleteEnglishSummary(context.Background(), englishTestSnapshot("korean note"), englishTestState(now), "english note", "source-hash", now); err != nil {
		t.Fatal(err)
	}
	meeting := (*ops)[0]["Update"].(map[string]any)
	condition := analysisCondition(t, meeting)
	for _, value := range []string{"transcriptA", "saved source", "notes", "selectedTranscript", "transcriptRevisionA", "transcriptSegments", "userId"} {
		if !strings.Contains(condition, value) {
			t.Fatalf("source pin %s missing: %s", value, condition)
		}
	}
	for _, name := range opNames(meeting) {
		if slices.Contains([]string{"content", "summaryPrivacy", "actionItems", "insights", "tags", "sentiment", "liveSummary", "actionItemContinuity", "summaryAccountApproval", "authorizationRevision", "insightsRevision"}, name) {
			t.Fatalf("English CAS pins Korean/derivative field %s: %s", name, condition)
		}
	}
	if strings.Contains(condition, "korean note") {
		t.Fatalf("English CAS pins Korean content: %s", condition)
	}
	run := analysisCondition(t, (*ops)[1]["Update"].(map[string]any))
	for _, value := range []string{"en-run", "running", "leaseUntil", "source-hash", "editor"} {
		if !strings.Contains(run, value) {
			t.Fatalf("run guard %s missing: %s", value, run)
		}
	}
	fences := map[string]bool{}
	for _, op := range (*ops)[2:] {
		pk, sk := opKey(op["ConditionCheck"].(map[string]any))
		fences[pk+"|"+sk] = true
	}
	for _, want := range []string{"USER#editor|SHARED#meeting", "MEETING#meeting|ATTACH#a", "MEETING#meeting|ATTEXT#a"} {
		if !fences[want] {
			t.Fatalf("missing fence %s: %v", want, fences)
		}
	}
	if fences["MEETING#meeting|"+model.ActionAnalysisSK] {
		t.Fatal("English CAS must not pin the Korean action-analysis row")
	}
}

func TestEnglishQueueAndStartFenceSourcesNotKoreanContent(t *testing.T) {
	now := time.Now().UTC()
	for name, write := range map[string]func(*DynamoDBRepository) error{
		"queue": func(r *DynamoDBRepository) error {
			next := englishTestState(now)
			next.Status, next.UpdatedAt = model.AnalysisQueued, now
			return r.QueueEnglishSummary(context.Background(), englishTestSnapshot("korean"), nil, next)
		},
		"start": func(r *DynamoDBRepository) error {
			return r.StartEnglishSummary(context.Background(), englishTestSnapshot("korean"), englishTestState(now), now, now.Add(time.Minute).UnixMilli())
		},
	} {
		t.Run(name, func(t *testing.T) {
			ops, repo := captureEnglishTransaction(t)
			if err := write(repo); err != nil {
				t.Fatal(err)
			}
			last := (*ops)[len(*ops)-1]["Update"].(map[string]any)
			if _, sk := opKey(last); sk != model.EnglishSummarySK {
				t.Fatalf("state write targets %s", sk)
			}
			for _, op := range (*ops)[:len(*ops)-1] {
				check := op["ConditionCheck"].(map[string]any)
				if _, sk := opKey(check); sk == model.ActionAnalysisSK {
					t.Fatal("English run must not fence Korean action analysis")
				}
				if slices.Contains(opNames(check), "content") {
					t.Fatal("English run must not pin Korean content")
				}
			}
		})
	}
}

func TestEnglishFailAndGetUseEnglishState(t *testing.T) {
	now := time.Now().UTC()
	var targets []string
	repo := actionAnalysisWire(t, func(target string, body map[string]any) {
		_, sk := opKey(body)
		targets = append(targets, target+"|"+sk)
	}, 200, `{}`)
	if err := repo.FailEnglishSummary(context.Background(), "meeting", englishTestState(now), "MODEL_ERROR", now); err != nil {
		t.Fatal(err)
	}
	if state, err := repo.GetEnglishSummary(context.Background(), "meeting"); err != nil || state != nil {
		t.Fatalf("absent English state: %v %v", state, err)
	}
	for _, target := range targets {
		if !strings.HasSuffix(target, "|"+model.EnglishSummarySK) {
			t.Fatalf("English call used %s", target)
		}
	}
	if len(targets) != 2 {
		t.Fatalf("calls: %v", targets)
	}
}

func TestEnglishWritesRejectBeforeSDK(t *testing.T) {
	repo := actionAnalysisWire(t, func(string, map[string]any) {
		t.Fatal("rejected English write reached DynamoDB")
	}, 200, `{}`)
	now := time.Now().UTC()
	missingSource := englishTestSnapshot("korean")
	delete(missingSource.Checks[0].Fields, "transcriptRevisionA")
	safe := englishTestState(now)
	safe.SharingSafe = true
	cases := map[string]func() error{
		"nil snapshot": func() error {
			return repo.CompleteEnglishSummary(context.Background(), nil, englishTestState(now), "en", "h", now)
		},
		"missing source": func() error {
			return repo.CompleteEnglishSummary(context.Background(), missingSource, englishTestState(now), "en", "h", now)
		},
		"sharing-safe complete": func() error {
			return repo.CompleteEnglishSummary(context.Background(), englishTestSnapshot("korean"), safe, "en", "h", now)
		},
		"sharing-safe queue": func() error {
			return repo.QueueEnglishSummary(context.Background(), englishTestSnapshot("korean"), nil, safe)
		},
		"sharing-safe start": func() error {
			return repo.StartEnglishSummary(context.Background(), englishTestSnapshot("korean"), safe, now, 1)
		},
		"missing-source queue": func() error {
			return repo.QueueEnglishSummary(context.Background(), missingSource, nil, englishTestState(now))
		},
	}
	for name, write := range cases {
		t.Run(name, func(t *testing.T) {
			if err := write(); !errors.Is(err, ErrConditionFailed) {
				t.Fatalf("want ErrConditionFailed, got %v", err)
			}
		})
	}
}

func TestCompleteEnglishSummaryTooLarge(t *testing.T) {
	repo := actionAnalysisWire(t, func(string, map[string]any) {
		t.Fatal("oversized English note reached DynamoDB")
	}, 200, `{}`)
	now := time.Now().UTC()
	snapshot := englishTestSnapshot(strings.Repeat("k", 290*1024))
	err := repo.CompleteEnglishSummary(context.Background(), snapshot, englishTestState(now), strings.Repeat("e", 20*1024), "source-hash", now)
	if !errors.Is(err, ErrSummaryLimit) {
		t.Fatalf("want ErrSummaryLimit, got %v", err)
	}
}

func TestKoreanResummaryCountsStoredEnglishNote(t *testing.T) {
	repo := actionAnalysisWire(t, func(string, map[string]any) {
		t.Fatal("oversized Korean note reached DynamoDB")
	}, 200, `{}`)
	now := time.Now().UTC()
	snapshot := englishTestSnapshot("old")
	snapshot.Stored["contentEn"] = strings.Repeat("e", 20*1024)
	err := repo.CompleteResummary(context.Background(), snapshot, englishTestState(now), strings.Repeat("k", 290*1024), "", "hash", now)
	if !errors.Is(err, ErrSummaryLimit) {
		t.Fatalf("want ErrSummaryLimit, got %v", err)
	}
}

// Korean capture/publication must not fence the English result, so an English
// publish never makes a Korean resummary, batch summary or grant fail.
func TestKoreanSummaryFencesExcludeEnglishNote(t *testing.T) {
	lists := map[string][]string{
		"resummary": withSummaryPrivacyFields(withSummaryClearanceFields(summaryMeetingFields)),
		"batch":     withSummaryPrivacyFields(withSummaryClearanceFields(batchSummaryFields)),
		"privacy":   withSummaryPrivacyFields(nil),
	}
	for name, fields := range lists {
		for _, field := range fields {
			if strings.HasPrefix(field, "contentEn") {
				t.Fatalf("%s fence pins %s", name, field)
			}
		}
	}
	for _, field := range englishSourceFields() {
		if field == "content" || strings.HasPrefix(field, "contentEn") {
			t.Fatalf("English source fence pins %s", field)
		}
	}
}

func TestEnglishNoteCountsAgainstItemBudget(t *testing.T) {
	if !slices.Contains(nonSpillableSizedFields, "contentEn") {
		t.Fatal("contentEn must count against the inline item budget")
	}
	if !sttDeletionAllowedChildren[model.EnglishSummarySK] {
		t.Fatal("English state must not block failed-recording deletion")
	}
}

func TestDeleteMeetingDeletesEnglishSummaryState(t *testing.T) {
	transactions := 0
	var first []string
	repo := actionAnalysisWire(t, func(target string, body map[string]any) {
		if !strings.HasSuffix(target, ".TransactWriteItems") {
			return
		}
		transactions++
		if transactions == 1 {
			for _, raw := range body["TransactItems"].([]any) {
				pk, sk := opKey(raw.(map[string]any)["Delete"].(map[string]any))
				first = append(first, pk+"|"+sk)
			}
		}
	}, 200, `{"Items":[]}`)
	if err := repo.DeleteMeeting(context.Background(), "owner", "meeting"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"USER#owner|MEETING#meeting", "MEETING#meeting|" + model.ResummarySK, "MEETING#meeting|" + model.EnglishSummarySK} {
		if !slices.Contains(first, want) {
			t.Fatalf("first transaction lacks %s: %v", want, first)
		}
	}
}

// A worker that outlives meeting deletion finds no state row, so every
// English state write conditioned on its run fails instead of recreating it.
func TestLateEnglishWorkerWriteRequiresLiveState(t *testing.T) {
	now := time.Now().UTC()
	ops, repo := captureEnglishTransaction(t)
	if err := repo.CompleteEnglishSummary(context.Background(), englishTestSnapshot("k"), englishTestState(now), "en", "h", now); err != nil {
		t.Fatal(err)
	}
	meeting := analysisCondition(t, (*ops)[0]["Update"].(map[string]any))
	run := analysisCondition(t, (*ops)[1]["Update"].(map[string]any))
	if !strings.Contains(meeting, "attribute_exists (PK)") || !strings.Contains(run, `"en-run"`) || strings.Contains(run, "attribute_not_exists (PK)") {
		t.Fatalf("late write could recreate rows:\nmeeting=%s\nrun=%s", meeting, run)
	}
}
