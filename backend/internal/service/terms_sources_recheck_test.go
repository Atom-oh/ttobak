package service

import (
	"context"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func TestProcessSourceRechecksTheSourceAfterExtraction(t *testing.T) {
	research := func(env *sourceTestEnv) {
		env.store.research["r1"] = &model.Research{ResearchID: "r1", UserID: "owner", Status: model.StatusDone,
			S3Key: "shared/research/r1.md", AccountIDs: []string{"a1"}}
		env.put("shared/research/r1.md", sourceTestText, nil)
	}
	cases := []struct {
		name    string
		ev      model.TermsSourceRequested
		setup   func(*sourceTestEnv)
		during  func(*sourceTestEnv)
		account bool // the Account still receives the term
		person  bool // the owner's Personal layer receives it
	}{
		{"research trashed", model.TermsSourceRequested{Kind: "research", Ref: "r1", OwnerID: "owner"}, research,
			func(e *sourceTestEnv) { e.store.research["r1"].TrashedAt = "2026-10-27T00:00:00Z" }, false, false},
		{"research unlinked", model.TermsSourceRequested{Kind: "research", Ref: "r1", OwnerID: "owner"}, research,
			func(e *sourceTestEnv) { e.store.research["r1"].AccountIDs = nil }, false, true},
		{"KB file deleted", model.TermsSourceRequested{Kind: "kb", Ref: sourceTestKBKey, OwnerID: "owner"},
			func(e *sourceTestEnv) { e.put(sourceTestKBKey, sourceTestText, map[string]string{"account-id": "a1"}) },
			func(e *sourceTestEnv) { delete(e.objects.objects, sourceTestBucket+"/"+sourceTestKBKey) }, false, false},
		{"KB file re-tagged", model.TermsSourceRequested{Kind: "kb", Ref: sourceTestKBKey, OwnerID: "owner"},
			func(e *sourceTestEnv) { e.put(sourceTestKBKey, sourceTestText, map[string]string{"account-id": "a1"}) },
			func(e *sourceTestEnv) { e.put(sourceTestKBKey, sourceTestText, nil) }, false, true},
		{"unchanged", model.TermsSourceRequested{Kind: "research", Ref: "r1", OwnerID: "owner"}, research, func(*sourceTestEnv) {}, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newSourceTestEnv(sourcesOn())
			tc.setup(env)
			env.members["a1"] = []string{"owner"}
			env.llm.outputs = []string{sourceTestOutput}
			env.llm.onCall = func() { tc.during(env) }
			if err := env.svc.ProcessSource(context.Background(), tc.ev); err != nil {
				t.Fatal(err)
			}
			if _, ok := env.term("ACCOUNT#a1", "AlphaGrid"); ok != tc.account {
				t.Fatalf("Account term present=%v, want %v", ok, tc.account)
			}
			if _, ok := env.term("USER#owner", "AlphaGrid"); ok != tc.person {
				t.Fatalf("Personal term present=%v, want %v", ok, tc.person)
			}
		})
	}
}

func TestProcessSourceEmptyObjectIsNoText(t *testing.T) {
	env := newSourceTestEnv(sourcesOn())
	env.put(sourceTestKBKey, "", nil)
	if err := env.svc.ProcessSource(context.Background(), model.TermsSourceRequested{Kind: "kb", Ref: sourceTestKBKey, OwnerID: "owner"}); err != nil {
		t.Fatalf("an empty object is terminal, not retried: %v", err)
	}
	if len(env.llm.requests) != 0 {
		t.Fatal("an empty object makes no model call")
	}
}
