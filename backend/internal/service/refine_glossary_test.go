package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// glossaryModel answers every refinement call with reply and records the
// request bodies it received.
func glossaryModel(t *testing.T, reply string, bodies *[]string) *BedrockService {
	t.Helper()
	return glossaryModelBy(t, reply, reply, bodies)
}

// glossaryModelBy answers glossary-prompt calls with glossaryReply and
// plain-prompt calls (including a re-run) with plainReply.
func glossaryModelBy(t *testing.T, glossaryReply, plainReply string, bodies *[]string) *BedrockService {
	t.Helper()
	return summaryTestService(noteSourceHTTPClient(func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		*bodies = append(*bodies, string(raw))
		reply := plainReply
		var request ClaudeRequest
		if json.Unmarshal(raw, &request) == nil && strings.Contains(request.System, glossaryRefineRule) {
			reply = glossaryReply
		}
		return indexHTTPResponse(200, refinementResponse(reply, "end_turn"), nil), nil
	}))
}

func kCultureGlossary() []GlossaryTerm {
	return []GlossaryTerm{{DisplayAs: "K-culture", HeardAs: []string{"케이 컬처"}, TermKey: TermKey("K-culture"), Layer: model.PrefixAccount + "a1"}}
}

func systemPromptOf(t *testing.T, body string) string {
	t.Helper()
	var request ClaudeRequest
	if err := json.Unmarshal([]byte(body), &request); err != nil {
		t.Fatal(err)
	}
	return request.System
}

func TestRefineGlossaryFixesMishearing(t *testing.T) {
	input := []WhisperSegment{{Start: 0, End: 2, Text: "we love gay culture", Speaker: "spk_0"}, {Start: 2, End: 4, Text: "and AlphaGrid", Speaker: "spk_0"}}
	reply := `{"segments":[{"speaker":"spk_0","text":"we love K-culture and AlphaGrid","startTime":0,"endTime":4,"sourceIds":[0,1]}],` +
		`"corrections":[{"from":"gay culture","to":"K-culture","segment":0}]}`
	var bodies []string
	svc := glossaryModel(t, reply, &bodies)
	text, segments, quality, corrections, err := svc.RefineTranscriptWithGlossary(context.Background(), input, kCultureGlossary())
	if err != nil || quality.Refinement != "validated" {
		t.Fatalf("refinement failed: %v %+v", err, quality)
	}
	if strings.Contains(text, "gay culture") || !strings.Contains(text, "K-culture") || segments[0].Text != "we love K-culture and AlphaGrid" {
		t.Fatalf("mishearing not corrected: %q", text)
	}
	want := []model.TranscriptCorrection{{From: "gay culture", To: "K-culture", TermKey: TermKey("K-culture"), Segment: 0}}
	if !reflect.DeepEqual(corrections, want) {
		t.Fatalf("corrections = %+v, want %+v", corrections, want)
	}
	if len(bodies) != 1 {
		t.Fatalf("one chunk, one call: %d", len(bodies))
	}
	system := systemPromptOf(t, bodies[0])
	rule := "Glossary terms may have been mis-heard as similar-sounding words. Replace a span only when it is a clear mis-hearing of a glossary term in context. Never add a glossary term where no similar-sounding span exists. Never paraphrase. Return each replacement in `corrections`."
	if !strings.Contains(system, rule) || !strings.Contains(system, "K-culture") || !strings.Contains(system, "케이 컬처") {
		t.Fatalf("glossary rule or terms missing from system prompt:\n%s", system)
	}
}

func TestRefineGlossaryNoSpanNoChange(t *testing.T) {
	weather := []WhisperSegment{{Start: 0, End: 2, Text: "we discussed the weather", Speaker: "spk_0"}}
	love := []WhisperSegment{{Start: 0, End: 2, Text: "we love gay culture", Speaker: "spk_0"}}
	segment := func(text string) string {
		return fmt.Sprintf(`{"speaker":"spk_0","text":%q,"startTime":0,"endTime":2,"sourceIds":[0]}`, text)
	}
	object := func(text, corrections string) string {
		return `{"segments":[` + segment(text) + `],"corrections":[` + corrections + `]}`
	}
	plainWeather, plainLove := "["+segment("we discussed the weather")+"]", "["+segment("we love gay culture")+"]"
	tests := []struct {
		name         string
		input        []WhisperSegment
		reply, plain string
		rerun        bool
		refinement   string
	}{
		// The model claims a fix for a span the original never had: the
		// record is dropped and the unchanged model text stands.
		{"absent-from", weather, object("we discussed the weather", `{"from":"gay culture","to":"K-culture","segment":0}`), plainWeather, false, "validated"},
		// A replacement to a form that is not a glossary display form is
		// reverted where it can be located once.
		{"not-glossary", weather, object("we discussed the Weather Inc", `{"from":"weather","to":"Weather Inc","segment":0}`), plainWeather, false, "validated"},
		// Claimed but never applied: no record, text untouched.
		{"not-applied", weather, object("we discussed the weather", `{"from":"weather","to":"K-culture","segment":0}`), plainWeather, false, "validated"},
		// Out-of-range source IDs cannot be checked.
		{"foreign-segment", weather, object("we discussed the weather", `{"from":"weather","to":"K-culture","segment":7}`), plainWeather, false, "validated"},
		// A bare array keeps the old contract: no corrections.
		{"bare-array", weather, plainWeather, plainWeather, false, "validated"},
		// R11: an invented glossary term never survives, with or without a
		// claim: the chunk is re-run on the plain prompt.
		{"invented-with-claim", weather, object("we discussed K-culture", `{"from":"gay culture","to":"K-culture","segment":0}`), plainWeather, true, "validated"},
		{"invented-unclaimed", weather, object("we discussed K-culture", ""), plainWeather, true, "validated"},
		// A claimed From that was not actually replaced backs nothing.
		{"from-not-replaced", love, object("we love K-culture", `{"from":"we","to":"K-culture","segment":0}`), plainLove, true, "validated"},
		// A failed re-run takes the raw fallback.
		{"rerun-fails", weather, object("we discussed K-culture", ""), "not json", true, "raw"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var bodies, plainBodies []string
			text, _, quality, corrections, err := glossaryModelBy(t, test.reply, test.plain, &bodies).RefineTranscriptWithGlossary(context.Background(), test.input, kCultureGlossary())
			if err != nil || quality.Refinement != test.refinement {
				t.Fatalf("refinement: %v %+v", err, quality)
			}
			wantText, _, _, err := glossaryModel(t, test.plain, &plainBodies).RefineTranscriptWithQuality(context.Background(), test.input)
			if err != nil {
				t.Fatal(err)
			}
			if !test.rerun {
				wantText = "[spk_0]\n" + test.input[0].Text
			}
			if text != wantText {
				t.Fatalf("text = %q, want %q", text, wantText)
			}
			if len(corrections) != 0 || corrections == nil {
				t.Fatalf("unverified correction kept (or glossary path not marked): %#v", corrections)
			}
			if wantCalls := map[bool]int{false: 1, true: 2}[test.rerun]; len(bodies) != wantCalls {
				t.Fatalf("model calls = %d, want %d", len(bodies), wantCalls)
			}
			if test.rerun && (systemPromptOf(t, bodies[1]) != buildRefineSystemPrompt(true) || bodies[1] != plainBodies[0]) {
				t.Fatal("the re-run must be exactly the plain refinement request")
			}
		})
	}
}

func TestRefineGlossaryCapsCorrectionsPerMeeting(t *testing.T) {
	var input []WhisperSegment
	var out, fixes []string
	for i := 0; i < model.MaxTranscriptCorrections+5; i++ {
		input = append(input, WhisperSegment{Start: float64(i), End: float64(i) + 1, Text: "gay culture", Speaker: "spk_0"})
		out = append(out, fmt.Sprintf(`{"speaker":"spk_0","text":"K-culture","startTime":%d,"endTime":%d,"sourceIds":[%d]}`, i, i+1, i))
		fixes = append(fixes, fmt.Sprintf(`{"from":"gay culture","to":"K-culture","segment":%d}`, i))
	}
	reply := `{"segments":[` + strings.Join(out, ",") + `],"corrections":[` + strings.Join(fixes, ",") + `]}`
	var bodies []string
	_, segments, _, corrections, err := glossaryModel(t, reply, &bodies).RefineTranscriptWithGlossary(context.Background(), input, kCultureGlossary())
	if err != nil || len(corrections) != model.MaxTranscriptCorrections || corrections[49].Segment != 49 {
		t.Fatalf("cap: %d %v", len(corrections), err)
	}
	if segments[len(segments)-1].Text != "K-culture" {
		t.Fatal("a verified correction beyond the record cap must keep its text")
	}
}

// Flag off and an empty glossary must send exactly today's request.
func TestRefineGlossaryOffIsByteIdentical(t *testing.T) {
	for preserve, want := range map[bool]string{
		true:  "032d0e6b543334af269c44fbc438b29fb723222864a8a2d2483cf4f8eeeae1f1",
		false: "b52f7d4431437b4404004f1e06395d3e53ec5fbf5bdbaa2ae58d75055c7dc8e4",
	} {
		sum := sha256.Sum256([]byte(buildRefineSystemPrompt(preserve)))
		if hex.EncodeToString(sum[:]) != want {
			t.Fatalf("refine system prompt changed (preserve=%v)", preserve)
		}
	}
	input := []WhisperSegment{{Start: 0, End: 2, Text: "we love gay culture", Speaker: "spk_0"}}
	reply := `[{"speaker":"spk_0","text":"we love gay culture","startTime":0,"endTime":2,"sourceIds":[0]}]`
	var plain, empty []string
	if _, _, _, err := glossaryModel(t, reply, &plain).RefineTranscriptWithQuality(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	_, _, _, corrections, err := glossaryModel(t, reply, &empty).RefineTranscriptWithGlossary(context.Background(), input, nil)
	if err != nil || corrections != nil {
		t.Fatalf("empty glossary: %v %+v", err, corrections)
	}
	if len(plain) != 1 || !reflect.DeepEqual(plain, empty) {
		t.Fatal("an empty glossary changed the refinement request")
	}
}

func TestGlossaryScopedToMeetingAccount(t *testing.T) {
	store := newLayerStore()
	store.seed("USER#u1", "AlphaGrid")
	store.seed(model.PrefixAccount+"a1", "K-culture")
	store.seed(model.PrefixAccount+"b1", "BetaNode")
	store.seed(model.PrefixDictCommon, "GammaWave")
	suggested := store.seed(model.PrefixDictCommon, "DeltaSuggested")
	suggested.State = model.TermStateSuggested
	store.put(suggested)
	edited := store.seed(model.PrefixDictCommon, "SigmaEdited")
	edited.UserEditedAt = "2026-10-26T00:00:00.000000Z"
	store.put(edited)
	heard := store.seed(model.PrefixAccount+"a1", "OmegaLink")
	heard.HeardAs = []string{"오메가 링크"}
	store.put(heard)
	store.members["a1/u1"], store.members["b1/u1"] = true, true
	svc := newLayerService(store)

	displays := func(terms []GlossaryTerm) map[string]GlossaryTerm {
		out := map[string]GlossaryTerm{}
		for _, term := range terms {
			out[term.DisplayAs] = term
		}
		return out
	}
	terms, err := svc.GlossaryForMeeting(context.Background(), "u1", "a1")
	if err != nil {
		t.Fatal(err)
	}
	got := displays(terms)
	for _, want := range []string{"AlphaGrid", "K-culture", "GammaWave", "OmegaLink"} {
		if _, ok := got[want]; !ok {
			t.Fatalf("%s missing from glossary: %+v", want, terms)
		}
	}
	if _, ok := got["BetaNode"]; ok {
		t.Fatal("another Account's term leaked into the meeting glossary")
	}
	if _, ok := got["DeltaSuggested"]; ok {
		t.Fatal("a suggested term is not active")
	}
	if _, ok := got["SigmaEdited"]; ok {
		t.Fatal("a user-edited Common term must not drive corrections (R17)")
	}
	if got["K-culture"].Layer != model.PrefixAccount+"a1" || got["AlphaGrid"].Layer != "USER#u1" || got["GammaWave"].Layer != model.PrefixDictCommon {
		t.Fatalf("layer attribution: %+v", terms)
	}
	if !reflect.DeepEqual(got["OmegaLink"].HeardAs, []string{"오메가 링크"}) {
		t.Fatalf("heardAs not carried: %+v", got["OmegaLink"])
	}

	// A former member gets no Account layer.
	delete(store.members, "a1/u1")
	terms, err = svc.GlossaryForMeeting(context.Background(), "u1", "a1")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := displays(terms)["K-culture"]; ok {
		t.Fatal("Account terms included without membership")
	}

	// At most 200 terms, Personal first.
	for i := 0; i < MaxGlossaryTerms+10; i++ {
		store.seed(model.PrefixDictCommon, fmt.Sprintf("Common%03d", i))
	}
	terms, err = svc.GlossaryForMeeting(context.Background(), "u1", "")
	if err != nil || len(terms) != MaxGlossaryTerms || terms[0].DisplayAs != "AlphaGrid" {
		t.Fatalf("cap/order: %d %v", len(terms), err)
	}
}

// memCorrectionStore mirrors the repository's feedback writes: a capped set
// of meeting IDs per counter row and a non-editing heardAs append over a
// term store, both membership-checked on ACCOUNT#.
type memCorrectionStore struct {
	rows    map[string]map[string]bool
	members map[string]bool
	terms   interface {
		GetDictTerm(context.Context, string, string) (*model.DictTerm, error)
	}
	put func(model.DictTerm)
}

func (c *memCorrectionStore) GetDictTerm(ctx context.Context, pk, key string) (*model.DictTerm, error) {
	return c.terms.GetDictTerm(ctx, pk, key)
}

func (c *memCorrectionStore) AppendHeardAs(ctx context.Context, pk, key, phrase, now string, v int64, member *repository.MembershipCheck) error {
	scope, accountID, _ := model.TermScopeOf(pk)
	if scope == model.TermScopeAccount && (member == nil || member.AccountID != accountID || !c.members[accountID+"/"+member.UserID]) {
		return repository.ErrNotAccountMember
	}
	row, _ := c.terms.GetDictTerm(ctx, pk, key)
	if row == nil || row.Version != v || row.State != model.TermStateActive {
		return conflict()
	}
	next := *row
	next.HeardAs = append(append([]string(nil), row.HeardAs...), phrase)
	next.UpdatedAt, next.Version = now, v+1
	c.put(next)
	return nil
}

func (c *memCorrectionStore) RecordTermCorrection(_ context.Context, pk, termKey, heardKey, meetingID string, expiresAt int64, member *repository.MembershipCheck) (int, error) {
	scope, accountID, _ := model.TermScopeOf(pk)
	if scope == model.TermScopeAccount && (member == nil || member.AccountID != accountID || !c.members[accountID+"/"+member.UserID]) {
		return 0, repository.ErrNotAccountMember
	}
	if expiresAt <= 0 {
		return 0, repository.ErrInvalidInput
	}
	key := pk + "|" + termKey + "|" + heardKey
	if c.rows[key] == nil {
		c.rows[key] = map[string]bool{}
	}
	if len(c.rows[key]) < model.MaxTermCorrectionMeetings {
		c.rows[key][meetingID] = true
	}
	return len(c.rows[key]), nil
}

func TestRepeatedCorrectionAddsHeardAs(t *testing.T) {
	store := newLayerStore()
	account := store.seed(model.PrefixAccount+"a1", "K-culture")
	personal := store.seed("USER#u1", "AlphaGrid")
	common := store.seed(model.PrefixDictCommon, "GammaWave")
	store.members["a1/u1"] = true
	svc := newLayerService(store)
	counter := &memCorrectionStore{rows: map[string]map[string]bool{}, members: store.members, terms: store, put: store.put}
	svc.SetCorrectionStore(counter)
	ctx := context.Background()

	glossary, err := svc.GlossaryForMeeting(ctx, "u1", "a1")
	if err != nil {
		t.Fatal(err)
	}
	corrections := []model.TranscriptCorrection{
		{From: "gay culture", To: "K-culture", TermKey: account.TermKey},
		{From: "Alpha Great", To: "AlphaGrid", TermKey: personal.TermKey},
		{From: "Gamma Wade", To: "GammaWave", TermKey: common.TermKey},
	}
	heard := func(pk, key string) []string {
		row, _ := store.row(pk, key)
		return row.HeardAs
	}

	svc.LearnFromCorrections(ctx, "u1", "a1", "m1", glossary, corrections)
	svc.LearnFromCorrections(ctx, "u1", "a1", "m1", glossary, corrections) // same meeting again
	if len(heard(account.PK, account.TermKey)) != 0 || len(heard(personal.PK, personal.TermKey)) != 0 {
		t.Fatal("one meeting must not teach heardAs")
	}

	svc.LearnFromCorrections(ctx, "u1", "a1", "m2", glossary, corrections)
	if got := heard(account.PK, account.TermKey); !reflect.DeepEqual(got, []string{"gay culture"}) {
		t.Fatalf("account heardAs = %v", got)
	}
	if got := heard(personal.PK, personal.TermKey); !reflect.DeepEqual(got, []string{"Alpha Great"}) {
		t.Fatalf("personal heardAs = %v", got)
	}
	// Common is never written automatically and gets no counter row (R6, R12).
	if got := heard(common.PK, common.TermKey); len(got) != 0 {
		t.Fatalf("common heardAs written: %v", got)
	}
	for key := range counter.rows {
		if strings.HasPrefix(key, model.PrefixDictCommon+"|") {
			t.Fatalf("common counter row written: %s", key)
		}
	}
	// The append is not a user edit.
	if row, _ := store.row(account.PK, account.TermKey); row.UserEditedAt != "" || row.UpdatedBy != "" || row.Origin != model.TermOriginAuto {
		t.Fatalf("automatic heardAs marked the term edited: %+v", row)
	}

	// Idempotent: a third meeting adds nothing new.
	svc.LearnFromCorrections(ctx, "u1", "a1", "m3", glossary, corrections)
	if got := heard(account.PK, account.TermKey); len(got) != 1 {
		t.Fatalf("duplicate heardAs: %v", got)
	}

	// A former member's corrections never write the Account layer.
	other := store.seed(model.PrefixAccount+"a1", "OmegaLink")
	delete(store.members, "a1/u1")
	lost := []model.TranscriptCorrection{{From: "Omega Lynx", To: "OmegaLink", TermKey: other.TermKey}}
	stale := append(glossary, GlossaryTerm{DisplayAs: "OmegaLink", TermKey: other.TermKey, Layer: other.PK})
	svc.LearnFromCorrections(ctx, "u1", "a1", "m4", stale, lost)
	svc.LearnFromCorrections(ctx, "u1", "a1", "m5", stale, lost)
	if got := heard(other.PK, other.TermKey); len(got) != 0 {
		t.Fatalf("non-member wrote account heardAs: %v", got)
	}
}

// memMergeCorrections adapts the merger's single-partition store for feedback.
type memMergeCorrections struct {
	*memDictStore
	rows map[string]map[string]bool
}

func (m *memMergeCorrections) RecordTermCorrection(_ context.Context, pk, termKey, heardKey, meetingID string, _ int64, _ *repository.MembershipCheck) (int, error) {
	key := pk + "|" + termKey + "|" + heardKey
	if m.rows[key] == nil {
		m.rows[key] = map[string]bool{}
	}
	m.rows[key][meetingID] = true
	return len(m.rows[key]), nil
}

func (m *memMergeCorrections) AppendHeardAs(_ context.Context, _, key, phrase, now string, v int64, _ *repository.MembershipCheck) error {
	row, ok := m.terms[model.DictTermSK(key)]
	if !ok || row.Version != v || row.State != model.TermStateActive {
		return memConflict()
	}
	row.HeardAs, row.UpdatedAt, row.Version = append(append([]string(nil), row.HeardAs...), phrase), now, v+1
	m.terms[row.SK] = row
	return nil
}

func TestLearnedHeardAsKeepsAutomaticMerges(t *testing.T) {
	store := newMemDictStore()
	store.seed(model.DictTerm{DisplayAs: "AlphaGrid", Origin: model.TermOriginAuto, State: model.TermStateActive, Hits: 1})
	svc := newTestDictService(newFakeDictStore(), nil)
	svc.SetCorrectionStore(&memMergeCorrections{memDictStore: store, rows: map[string]map[string]bool{}})
	key := TermKey("AlphaGrid")
	glossary := []GlossaryTerm{{DisplayAs: "AlphaGrid", TermKey: key, Layer: "USER#owner"}}
	fix := []model.TranscriptCorrection{{From: "Alpha Great", To: "AlphaGrid", TermKey: key}}
	svc.LearnFromCorrections(context.Background(), "owner", "", "m1", glossary, fix)
	if added := svc.LearnFromCorrections(context.Background(), "owner", "", "m2", glossary, fix); added != 1 {
		t.Fatalf("added = %d", added)
	}
	row, _ := store.term("AlphaGrid")
	if row.UserEditedAt != "" || !reflect.DeepEqual(row.HeardAs, []string{"Alpha Great"}) {
		t.Fatalf("append: %+v", row)
	}
	res := mustMerge(t, newTestMerger(store), ref("C"), cand("AlphaGrid"))
	row, _ = store.term("AlphaGrid")
	if res.Counters.Merged != 1 || row.Hits != 2 || res.Counters.SkippedManual != 0 {
		t.Fatalf("a later automatic merge must still apply: %+v %+v", res.Counters, row)
	}
}
