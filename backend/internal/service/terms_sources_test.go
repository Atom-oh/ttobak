package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/ttobak/backend/internal/model"
)

const (
	sourceTestBucket = "kb-bucket"
	sourceTestKBKey  = "kb/owner/1700000000000_alphagrid-notes.md"
	sourceTestText   = "# AlphaGrid 운영 노트\nAlphaGrid 클러스터는 매일 점검합니다.\n"
	sourceTestOutput = `{"terms":[{"displayAs":"AlphaGrid","heardAs":["알파그리드"],"type":"product","phrase":"","confidence":0.95,` +
		`"evidence":[{"source":"document","documentId":"source-1","quote":"AlphaGrid 클러스터는"}]}]}`
)

type sourceStoreFake struct {
	runs     map[string]model.TermsSourceRun
	research map[string]*model.Research
	puts     int
}

func (f *sourceStoreFake) GetTermsSourceRun(_ context.Context, ownerID, kind, ref string) (*model.TermsSourceRun, error) {
	if run, ok := f.runs[ownerID+"|"+kind+"|"+ref]; ok {
		return &run, nil
	}
	return nil, nil
}

func (f *sourceStoreFake) PutTermsSourceRun(_ context.Context, run *model.TermsSourceRun) error {
	f.puts++
	f.runs[run.OwnerID+"|"+run.Kind+"|"+run.Ref] = *run
	return nil
}

func (f *sourceStoreFake) GetResearchByID(_ context.Context, id string) (*model.Research, error) {
	return f.research[id], nil
}

type sourceObject struct {
	body string
	meta map[string]string
}

type sourceObjectsFake struct {
	objects     map[string]sourceObject
	heads, gets int
}

func (f *sourceObjectsFake) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	f.heads++
	obj, ok := f.objects[aws.ToString(in.Bucket)+"/"+aws.ToString(in.Key)]
	if !ok {
		return nil, &s3types.NotFound{}
	}
	return &s3.HeadObjectOutput{Metadata: obj.meta, ContentLength: aws.Int64(int64(len(obj.body)))}, nil
}

func (f *sourceObjectsFake) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	f.gets++
	obj, ok := f.objects[aws.ToString(in.Bucket)+"/"+aws.ToString(in.Key)]
	if !ok {
		return nil, &s3types.NoSuchKey{}
	}
	return &s3.GetObjectOutput{Metadata: obj.meta, Body: io.NopCloser(strings.NewReader(obj.body))}, nil
}

type sourceTestEnv struct {
	svc       *TermsAnalysisService
	dict      partitionedDictStore
	store     *sourceStoreFake
	objects   *sourceObjectsFake
	llm       *termsModelFake
	published []model.TermsSourceRequested
	members   map[string][]string
	refreshed []string
}

func newSourceTestEnv(cfg TermsConfig) *sourceTestEnv {
	env := &sourceTestEnv{
		dict:    partitionedDictStore{},
		store:   &sourceStoreFake{runs: map[string]model.TermsSourceRun{}, research: map[string]*model.Research{}},
		objects: &sourceObjectsFake{objects: map[string]sourceObject{}},
		llm:     &termsModelFake{},
		members: map[string][]string{},
	}
	merger := NewTermsMerger(env.dict)
	merger.now = func() time.Time { return termsTestNow }
	env.svc, _ = newTermsTestService(&termsRepoFake{}, env.llm, merger, cfg)
	env.svc.SetAccountMemberLister(func(_ context.Context, accountID string) ([]string, error) {
		return env.members[accountID], nil
	})
	env.svc.SetVocabularyRefresher(func(_ context.Context, ownerID string) { env.refreshed = append(env.refreshed, ownerID) })
	env.svc.SetSourceReader(TermsSourceDeps{
		Store: env.store, Objects: env.objects, Bucket: sourceTestBucket,
		Publish: func(_ context.Context, ev model.TermsSourceRequested) error {
			env.published = append(env.published, ev)
			return nil
		},
	})
	return env
}

func (e *sourceTestEnv) put(key, body string, meta map[string]string) {
	e.objects.objects[sourceTestBucket+"/"+key] = sourceObject{body: body, meta: meta}
}

func (e *sourceTestEnv) term(pk, display string) (model.DictTerm, bool) {
	return e.dict.at(pk).term(display)
}

func sourcesOn() TermsConfig {
	cfg := termsOn
	cfg.KBMode, cfg.ResearchMode = SourceTermsModeOn, SourceTermsModeOn
	return cfg
}

func TestProcessSourceResearchToAccounts(t *testing.T) {
	env := newSourceTestEnv(sourcesOn())
	env.store.research["r1"] = &model.Research{ResearchID: "r1", UserID: "owner", Status: model.StatusDone,
		S3Key: "shared/research/r1.md", AccountIDs: []string{"a1", "a2"}}
	env.put("shared/research/r1.md", sourceTestText, nil)
	env.members["a1"] = []string{"owner", "u2"}
	env.members["a2"] = []string{"u3"}
	env.llm.outputs = []string{sourceTestOutput}

	if err := env.svc.ProcessSource(context.Background(), model.TermsSourceRequested{Kind: "research", Ref: "r1", OwnerID: "owner"}); err != nil {
		t.Fatal(err)
	}
	got, ok := env.term("ACCOUNT#a1", "AlphaGrid")
	if !ok || got.State != model.TermStateActive || got.Origin != model.TermOriginAuto {
		t.Fatalf("the member Account must receive the term: %+v ok=%v", got, ok)
	}
	if len(got.Sources) != 1 || got.Sources[0].Kind != model.DictSourceResearch || got.Sources[0].Ref != "r1" || got.FirstSourceMeetingID != "" {
		t.Fatalf("source ref: %+v first=%q", got.Sources, got.FirstSourceMeetingID)
	}
	if len(got.HeardAs) != 0 {
		t.Fatalf("a document source has no transcript to ground aliases: %v", got.HeardAs)
	}
	if _, ok := env.term("ACCOUNT#a2", "AlphaGrid"); ok {
		t.Fatal("an Account the owner is not a member of must not be written")
	}
	if _, ok := env.term("USER#owner", "AlphaGrid"); ok {
		t.Fatal("a member Account took the terms; Personal is only the fallback")
	}
	if _, ok := env.dict[model.PrefixDictCommon]; ok {
		t.Fatal("research never writes Common")
	}
	for sk, obs := range env.dict.at("ACCOUNT#a1").obs {
		if obs.MeetingID != "" || obs.SourceKind != model.DictSourceResearch || obs.SourceRef != "r1" || obs.OwnerID != "owner" ||
			sk != model.DictObsSK(got.TermKey, model.DictObsSourceID(model.DictSourceResearch, "r1")) {
			t.Fatalf("observation %s: %+v", sk, obs)
		}
	}
	var input termsCallInput
	if len(env.llm.requests) != 1 || json.Unmarshal([]byte(env.llm.requests[0].Messages[0].Content[0].Text), &input) != nil {
		t.Fatalf("one model call expected: %d", len(env.llm.requests))
	}
	if input.TranscriptChunk != "" || input.Notes != "" || len(input.Documents) != 1 || input.Documents[0].Text != sourceTestText {
		t.Fatalf("the report must be sent as a document: %+v", input)
	}
	if run := env.store.runs["owner|research|r1"]; run.ContentHash == "" {
		t.Fatal("the run row must record the content hash")
	}
	if strings.Join(env.refreshed, ",") != "owner,u2" {
		t.Fatalf("members of the written Account refresh: %v", env.refreshed)
	}
}

func TestProcessSourceKBPersonalWhenNoAccount(t *testing.T) {
	env := newSourceTestEnv(sourcesOn())
	env.put(sourceTestKBKey, sourceTestText, nil)
	env.llm.outputs = []string{sourceTestOutput}

	if err := env.svc.ProcessSource(context.Background(), model.TermsSourceRequested{Kind: "kb", Ref: sourceTestKBKey, OwnerID: "owner"}); err != nil {
		t.Fatal(err)
	}
	got, ok := env.term("USER#owner", "AlphaGrid")
	if !ok || got.State != model.TermStateActive || len(got.Sources) != 1 || got.Sources[0].Kind != model.DictSourceKB || got.Sources[0].Ref != sourceTestKBKey {
		t.Fatalf("personal term: %+v ok=%v", got, ok)
	}
	for sk := range env.dict.at("USER#owner").obs {
		if strings.Contains(sk, "alphagrid-notes") {
			t.Fatalf("the observation key must not embed the object key: %s", sk)
		}
	}
	if strings.Join(env.refreshed, ",") != "owner" {
		t.Fatalf("refresh: %v", env.refreshed)
	}
}

func TestProcessSourceKBAccountNeedsMembership(t *testing.T) {
	env := newSourceTestEnv(sourcesOn())
	env.put(sourceTestKBKey, sourceTestText, map[string]string{"account-id": "a1"})
	env.llm.outputs = []string{sourceTestOutput, sourceTestOutput}

	// The uploader left the Account: the document stays Personal.
	env.members["a1"] = []string{"u2"}
	if err := env.svc.ProcessSource(context.Background(), model.TermsSourceRequested{Kind: "kb", Ref: sourceTestKBKey, OwnerID: "owner", AccountIDs: []string{"a1"}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := env.term("ACCOUNT#a1", "AlphaGrid"); ok {
		t.Fatal("a non-member's upload must not reach the Account")
	}
	if _, ok := env.term("USER#owner", "AlphaGrid"); !ok {
		t.Fatal("a non-member's upload falls back to Personal")
	}

	other := "kb/owner/1700000000001_alphagrid-notes.md"
	env.put(other, sourceTestText+"추가\n", map[string]string{"account-id": "a1"})
	env.members["a1"] = []string{"owner"}
	if err := env.svc.ProcessSource(context.Background(), model.TermsSourceRequested{Kind: "kb", Ref: other, OwnerID: "owner"}); err != nil {
		t.Fatal(err)
	}
	if got, ok := env.term("ACCOUNT#a1", "AlphaGrid"); !ok || got.Sources[0].Ref != other {
		t.Fatalf("a member's upload is merged into the chosen Account (metadata is authoritative): %+v", got)
	}
}

func TestProcessSourceIdempotent(t *testing.T) {
	env := newSourceTestEnv(sourcesOn())
	env.put(sourceTestKBKey, sourceTestText, nil)
	env.llm.outputs = []string{sourceTestOutput, sourceTestOutput}
	ev := model.TermsSourceRequested{Kind: "kb", Ref: sourceTestKBKey, OwnerID: "owner"}
	for i := 0; i < 2; i++ {
		if err := env.svc.ProcessSource(context.Background(), ev); err != nil {
			t.Fatal(err)
		}
	}
	if len(env.llm.requests) != 1 || env.store.puts != 1 {
		t.Fatalf("an unchanged source must not run again: calls=%d puts=%d", len(env.llm.requests), env.store.puts)
	}
	if got, _ := env.term("USER#owner", "AlphaGrid"); got.Hits != 1 {
		t.Fatalf("hits=%d", got.Hits)
	}
	env.put(sourceTestKBKey, sourceTestText+"AlphaGrid 백업\n", nil)
	if err := env.svc.ProcessSource(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if len(env.llm.requests) != 2 {
		t.Fatalf("changed content runs again: calls=%d", len(env.llm.requests))
	}
	if got, _ := env.term("USER#owner", "AlphaGrid"); got.Hits != 1 {
		t.Fatalf("the same source is counted once: hits=%d", got.Hits)
	}
}

func TestProcessSourceDisabledLogs(t *testing.T) {
	for _, tc := range []struct {
		kind, ref string
		cfg       TermsConfig
	}{
		{"kb", sourceTestKBKey, termsOn},
		{"research", "r1", termsOn},
		{"kb", sourceTestKBKey, func() TermsConfig { c := sourcesOn(); c.Users = map[string]bool{"someone-else": true}; return c }()},
	} {
		env := newSourceTestEnv(tc.cfg)
		env.put(sourceTestKBKey, sourceTestText, nil)
		logs := capturePrivacyLogs(t)
		if err := env.svc.ProcessSource(context.Background(), model.TermsSourceRequested{Kind: tc.kind, Ref: tc.ref, OwnerID: "owner"}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(logs.String(), "kind="+tc.kind) || !strings.Contains(logs.String(), "outcome=DISABLED") {
			t.Fatalf("a skipped run must log DISABLED: %q", logs.String())
		}
		if env.objects.gets != 0 || len(env.llm.requests) != 0 || env.store.puts != 0 {
			t.Fatal("a disabled source must do no work")
		}
	}
}

func TestProcessSourceSkips(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(*sourceTestEnv)
		ev      model.TermsSourceRequested
		outcome string
		wantErr bool
	}{
		{"unsupported KB type", func(e *sourceTestEnv) { e.put("kb/owner/1_deck.pdf", "%PDF", nil) },
			model.TermsSourceRequested{Kind: "kb", Ref: "kb/owner/1_deck.pdf", OwnerID: "owner"}, "UNSUPPORTED", false},
		{"KB object of another owner", func(e *sourceTestEnv) { e.put("kb/u2/1_a.md", sourceTestText, nil) },
			model.TermsSourceRequested{Kind: "kb", Ref: "kb/u2/1_a.md", OwnerID: "owner"}, "INVALID", false},
		{"missing KB object", func(*sourceTestEnv) {},
			model.TermsSourceRequested{Kind: "kb", Ref: sourceTestKBKey, OwnerID: "owner"}, "NOT_FOUND", false},
		{"binary KB text", func(e *sourceTestEnv) { e.put(sourceTestKBKey, "\xff\xfe\x00", nil) },
			model.TermsSourceRequested{Kind: "kb", Ref: sourceTestKBKey, OwnerID: "owner"}, "UNSUPPORTED", false},
		{"research of another owner", func(e *sourceTestEnv) {
			e.store.research["r1"] = &model.Research{ResearchID: "r1", UserID: "u2", Status: model.StatusDone, S3Key: "shared/research/r1.md"}
		}, model.TermsSourceRequested{Kind: "research", Ref: "r1", OwnerID: "owner"}, "NOT_FOUND", false},
		{"research not done yet is retried", func(e *sourceTestEnv) {
			e.store.research["r1"] = &model.Research{ResearchID: "r1", UserID: "owner", Status: "running"}
		}, model.TermsSourceRequested{Kind: "research", Ref: "r1", OwnerID: "owner"}, "NOT_READY", true},
		{"news is not a source here", func(*sourceTestEnv) {},
			model.TermsSourceRequested{Kind: "news", Ref: "n1", OwnerID: "owner"}, "INVALID", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newSourceTestEnv(sourcesOn())
			tc.setup(env)
			logs := capturePrivacyLogs(t)
			err := env.svc.ProcessSource(context.Background(), tc.ev)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v", err)
			}
			if !strings.Contains(logs.String(), "outcome="+tc.outcome) {
				t.Fatalf("want outcome=%s in %q", tc.outcome, logs.String())
			}
			if len(env.llm.requests) != 0 || env.store.puts != 0 {
				t.Fatal("a skipped source must not call the model or record a run")
			}
			if strings.Contains(logs.String(), "alphagrid") || strings.Contains(logs.String(), "deck.pdf") {
				t.Fatalf("logs must not carry object names: %q", logs.String())
			}
		})
	}
}

func TestProcessSourceProviderFailureIsRetried(t *testing.T) {
	env := newSourceTestEnv(sourcesOn())
	env.put(sourceTestKBKey, sourceTestText, nil)
	env.llm.err = errors.New("throttled")
	if err := env.svc.ProcessSource(context.Background(), model.TermsSourceRequested{Kind: "kb", Ref: sourceTestKBKey, OwnerID: "owner"}); err == nil {
		t.Fatal("a provider failure must be returned for redelivery")
	}
	if env.store.puts != 0 {
		t.Fatal("a failed run must not record the content hash")
	}
}

func TestRequestSourceFromObject(t *testing.T) {
	cases := []struct {
		name    string
		cfg     TermsConfig
		setup   func(*sourceTestEnv)
		bucket  string
		key     string
		outcome string
		want    *model.TermsSourceRequested
	}{
		{"KB upload with an Account", sourcesOn(), func(e *sourceTestEnv) { e.put(sourceTestKBKey, sourceTestText, map[string]string{"account-id": "a1"}) },
			sourceTestBucket, sourceTestKBKey, "QUEUED",
			&model.TermsSourceRequested{Kind: "kb", Ref: sourceTestKBKey, OwnerID: "owner", AccountIDs: []string{"a1"}}},
		{"KB upload without an Account", sourcesOn(), func(e *sourceTestEnv) { e.put(sourceTestKBKey, sourceTestText, nil) },
			sourceTestBucket, sourceTestKBKey, "QUEUED",
			&model.TermsSourceRequested{Kind: "kb", Ref: sourceTestKBKey, OwnerID: "owner"}},
		{"KB upload with a malformed Account", sourcesOn(), func(e *sourceTestEnv) { e.put(sourceTestKBKey, sourceTestText, map[string]string{"account-id": "a#1"}) },
			sourceTestBucket, sourceTestKBKey, "QUEUED",
			&model.TermsSourceRequested{Kind: "kb", Ref: sourceTestKBKey, OwnerID: "owner"}},
		{"research report", sourcesOn(), func(e *sourceTestEnv) {
			e.store.research["r1"] = &model.Research{ResearchID: "r1", UserID: "owner", AccountIDs: []string{"a1", "a2"}}
		}, sourceTestBucket, "shared/research/r1.md", "QUEUED",
			&model.TermsSourceRequested{Kind: "research", Ref: "r1", OwnerID: "owner", AccountIDs: []string{"a1", "a2"}}},
		{"research section file", sourcesOn(), func(*sourceTestEnv) {}, sourceTestBucket, "shared/research/r1/overview.md", "SKIPPED", nil},
		{"unknown research", sourcesOn(), func(*sourceTestEnv) {}, sourceTestBucket, "shared/research/r9.md", "NOT_FOUND", nil},
		{"unsupported KB type", sourcesOn(), func(*sourceTestEnv) {}, sourceTestBucket, "kb/owner/1_deck.pdf", "UNSUPPORTED", nil},
		{"KB key without an owner", sourcesOn(), func(*sourceTestEnv) {}, sourceTestBucket, "kb/notes.md", "SKIPPED", nil},
		{"another bucket", sourcesOn(), func(*sourceTestEnv) {}, "assets", sourceTestKBKey, "SKIPPED", nil},
		{"KB disabled", termsOn, func(*sourceTestEnv) {}, sourceTestBucket, sourceTestKBKey, "DISABLED", nil},
		{"research disabled", termsOn, func(*sourceTestEnv) {}, sourceTestBucket, "shared/research/r1.md", "DISABLED", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newSourceTestEnv(tc.cfg)
			tc.setup(env)
			logs := capturePrivacyLogs(t)
			if err := env.svc.RequestSourceFromObject(context.Background(), tc.bucket, tc.key); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(logs.String(), "outcome="+tc.outcome) {
				t.Fatalf("want outcome=%s in %q", tc.outcome, logs.String())
			}
			if tc.want == nil {
				if len(env.published) != 0 {
					t.Fatalf("nothing must be published: %+v", env.published)
				}
				return
			}
			if len(env.published) != 1 {
				t.Fatalf("published %+v", env.published)
			}
			got := env.published[0]
			if got.Kind != tc.want.Kind || got.Ref != tc.want.Ref || got.OwnerID != tc.want.OwnerID ||
				strings.Join(got.AccountIDs, ",") != strings.Join(tc.want.AccountIDs, ",") {
				t.Fatalf("got %+v want %+v", got, *tc.want)
			}
			if tc.want.Kind == "kb" && env.objects.heads != 1 {
				t.Fatalf("the KB mapper reads the account-id metadata with HeadObject: heads=%d", env.objects.heads)
			}
		})
	}
}

func TestTermsSourceModesFromEnv(t *testing.T) {
	cases := []struct{ kb, research, wantKB, wantResearch string }{
		{"", "", SourceTermsModeOff, SourceTermsModeOff},
		{"on", " on ", SourceTermsModeOn, SourceTermsModeOn},
		{"true", "all", SourceTermsModeOff, SourceTermsModeOff},
	}
	for _, tc := range cases {
		t.Setenv("KB_TERMS_MODE", tc.kb)
		t.Setenv("RESEARCH_TERMS_MODE", tc.research)
		cfg := TermsConfigFromEnv()
		if cfg.KBMode != tc.wantKB || cfg.ResearchMode != tc.wantResearch {
			t.Fatalf("%q/%q -> %q/%q", tc.kb, tc.research, cfg.KBMode, cfg.ResearchMode)
		}
	}
	cfg := sourcesOn()
	if !cfg.SourceEnabled(model.DictSourceKB, "owner") || !cfg.SourceEnabled(model.DictSourceResearch, "owner") ||
		cfg.SourceEnabled(model.DictSourceNews, "owner") || cfg.SourceEnabled(model.DictSourceMeeting, "owner") || cfg.SourceEnabled(model.DictSourceKB, "") {
		t.Fatal("SourceEnabled covers kb and research only")
	}
}
