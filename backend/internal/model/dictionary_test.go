package model

import (
	"fmt"
	"testing"
)

func TestTermScopeOf(t *testing.T) {
	tests := []struct {
		pk        string
		scope     string
		accountID string
		ok        bool
	}{
		{"USER#u1", TermScopeUser, "", true},
		{"ACCOUNT#a1", TermScopeAccount, "a1", true},
		{"DICT#COMMON", TermScopeCommon, "", true},
		{"MEETING#x", "", "", false},
		{"", "", "", false},
	}
	for _, tc := range tests {
		scope, accountID, ok := TermScopeOf(tc.pk)
		if scope != tc.scope || accountID != tc.accountID || ok != tc.ok {
			t.Errorf("TermScopeOf(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.pk, scope, accountID, ok, tc.scope, tc.accountID, tc.ok)
		}
	}
}

func TestDictSourceRefsCapped(t *testing.T) {
	var refs []DictSourceRef
	for i := 0; i < 12; i++ {
		refs = AppendDictSource(refs, DictSourceRef{Kind: DictSourceMeeting, Ref: fmt.Sprintf("m%d", i)})
	}
	if len(refs) != 10 {
		t.Fatalf("len = %d, want 10", len(refs))
	}
	if refs[0].Ref != "m11" || refs[9].Ref != "m2" {
		t.Errorf("order = %s..%s, want m11..m2", refs[0].Ref, refs[9].Ref)
	}

	// Same kind+ref moves to the front with the new timestamp, no duplicate.
	refs = AppendDictSource(refs, DictSourceRef{Kind: DictSourceMeeting, Ref: "m5", At: "t2"})
	if len(refs) != 10 || refs[0].Ref != "m5" || refs[0].At != "t2" {
		t.Errorf("dedupe: len=%d first=%+v", len(refs), refs[0])
	}
	n := 0
	for _, r := range refs {
		if r.Ref == "m5" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("m5 occurrences = %d, want 1", n)
	}

	// Same ref under a different kind is a distinct source.
	refs = AppendDictSource(refs, DictSourceRef{Kind: DictSourceKB, Ref: "m5"})
	if refs[0].Kind != DictSourceKB || refs[1].Kind != DictSourceMeeting || refs[1].Ref != "m5" {
		t.Errorf("kind distinct: %+v %+v", refs[0], refs[1])
	}
}

func TestDictLogTrimSKsPinsOldest(t *testing.T) {
	sk := func(i int) string { return DictLogSK("k", fmt.Sprintf("2026-10-%02dT00:00:00.000000Z", i)) }
	var sks []string
	for i := 25; i >= 1; i-- { // any order
		sks = append(sks, sk(i))
	}
	trimmed := DictLogTrimSKs(sks, MaxDictLogsPerTerm)
	if len(trimmed) != 5 {
		t.Fatalf("trim %d rows, want 5: %v", len(trimmed), trimmed)
	}
	for _, got := range trimmed {
		if got == sk(1) {
			t.Fatal("the oldest row is pinned")
		}
		if got > sk(6) {
			t.Fatalf("trimmed a row newer than the oldest unpinned ones: %s", got)
		}
	}
	if got := DictLogTrimSKs(sks[:MaxDictLogsPerTerm], MaxDictLogsPerTerm); got != nil {
		t.Fatalf("nothing to trim at the cap: %v", got)
	}
	if got := DictLogTrimSKs([]string{sk(2), sk(1), sk(3)}, 0); len(got) != 2 || got[0] != sk(3) || got[1] != sk(2) {
		t.Fatalf("keep 0 still pins the oldest: %v", got)
	}
}
