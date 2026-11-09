package model

import (
	"strings"
	"testing"
)

func TestValidateAccountLink(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   AccountLinkInput
		ok   bool
	}{
		{"news to project", AccountLinkInput{"news", "acct_a_00000001:0123456789abcdef", "project", "p1"}, true},
		{"tech to project", AccountLinkInput{"tech", "__tech__:abc", "project", "p1"}, true},
		{"event to project", AccountLinkInput{"event", "e1", "project", "p1"}, true},
		{"news to meeting", AccountLinkInput{"news", "s:h", "meeting", "owner:m1"}, true},
		{"meeting to project rejected", AccountLinkInput{"meeting", "owner:m1", "project", "p1"}, false},
		{"project to news rejected", AccountLinkInput{"project", "p1", "news", "s:h"}, false},
		{"tech to meeting rejected", AccountLinkInput{"tech", "__tech__:abc", "meeting", "o:m"}, false},
		{"unknown kind", AccountLinkInput{"doc", "x", "project", "p1"}, false},
		{"hash in from id", AccountLinkInput{"news", "s#h", "project", "p1"}, false},
		{"hash in to id", AccountLinkInput{"event", "e1", "project", "p#1"}, false},
		{"empty id", AccountLinkInput{"event", "", "project", "p1"}, false},
		{"too long id", AccountLinkInput{"event", strings.Repeat("e", MaxAccountLinkIDBytes+1), "project", "p1"}, false},
		{"max length id", AccountLinkInput{"event", strings.Repeat("e", MaxAccountLinkIDBytes), "project", "p1"}, true},
		{"control char", AccountLinkInput{"event", "e\n1", "project", "p1"}, false},
	} {
		err := ValidateAccountLink(tc.in)
		if (err == nil) != tc.ok {
			t.Errorf("%s: got %v", tc.name, err)
		}
	}
}

func TestAccountLinkSortKeysRoundTrip(t *testing.T) {
	in := AccountLinkInput{"news", "acct_a_1:0123456789abcdef", "meeting", "owner:m1"}
	if got := AccountLinkSortKey(in); got != "LINK#news#acct_a_1:0123456789abcdef#meeting#owner:m1" {
		t.Fatalf("forward key %q", got)
	}
	if got := AccountLinkToSortKey(in); got != "LINKTO#meeting#owner:m1#news#acct_a_1:0123456789abcdef" {
		t.Fatalf("reverse key %q", got)
	}
	if got := AccountLinkDenySortKey(in); got != "LINKDENY#news#acct_a_1:0123456789abcdef#meeting#owner:m1" {
		t.Fatalf("deny key %q", got)
	}
	parsed, ok := ParseAccountLinkSortKey(AccountLinkSortKey(in))
	if !ok || parsed != in {
		t.Fatalf("parse %v %v", parsed, ok)
	}
	for _, bad := range []string{"LINKTO#meeting#o:m#news#s:h", "LINK#meeting#o:m#project#p", "LINK#news#s:h#project", "LINK#news#s:h#project#p#x"} {
		if _, ok := ParseAccountLinkSortKey(bad); ok {
			t.Fatalf("%q must not parse", bad)
		}
	}
}
