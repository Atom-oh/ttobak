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
