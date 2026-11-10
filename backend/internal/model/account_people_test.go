package model

import (
	"errors"
	"regexp"
	"testing"
)

func TestValidatePeopleLinkKey(t *testing.T) {
	ok := PeopleLinkKey{MeetingID: "u1:m1", ToKind: LinkKindContact, ToID: "c1", Role: PeopleRoleSpeaker}
	with := func(f func(*PeopleLinkKey)) PeopleLinkKey { k := ok; f(&k); return k }
	cases := []struct {
		name string
		key  PeopleLinkKey
		ok   bool
	}{
		{"speaker contact", ok, true},
		{"participant member", with(func(k *PeopleLinkKey) { k.Role = PeopleRoleParticipant; k.ToKind = LinkKindMember }), true},
		{"assignee contact", with(func(k *PeopleLinkKey) { k.Role = PeopleRoleAssignee }), true},
		{"mentions project", with(func(k *PeopleLinkKey) { k.Role = PeopleRoleMentions; k.ToKind = LinkKindProject }), true},
		{"mentions account", with(func(k *PeopleLinkKey) { k.Role = PeopleRoleMentions; k.ToKind = LinkKindAccount }), true},
		{"speaker project", with(func(k *PeopleLinkKey) { k.ToKind = LinkKindProject }), false},
		{"participant account", with(func(k *PeopleLinkKey) { k.Role = PeopleRoleParticipant; k.ToKind = LinkKindAccount }), false},
		{"mention kind is deny only", with(func(k *PeopleLinkKey) { k.Role = PeopleRoleMentions; k.ToKind = LinkKindMention }), false},
		{"unknown role", with(func(k *PeopleLinkKey) { k.Role = "owner" }), false},
		{"hash in owner", with(func(k *PeopleLinkKey) { k.MeetingID = "u#1:m1" }), false},
		{"hash in meeting", with(func(k *PeopleLinkKey) { k.MeetingID = "u1:m#1" }), false},
		{"hash in target", with(func(k *PeopleLinkKey) { k.ToID = "c#1" }), false},
		{"meeting without colon", with(func(k *PeopleLinkKey) { k.MeetingID = "m1" }), false},
		{"meeting with two colons", with(func(k *PeopleLinkKey) { k.MeetingID = "u1:m1:x" }), false},
		{"empty owner", with(func(k *PeopleLinkKey) { k.MeetingID = ":m1" }), false},
		{"empty meeting", with(func(k *PeopleLinkKey) { k.MeetingID = "u1:" }), false},
		{"target with colon", with(func(k *PeopleLinkKey) { k.ToID = "c:1" }), false},
		{"empty target", with(func(k *PeopleLinkKey) { k.ToID = "" }), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePeopleLinkKey(tc.key)
			if tc.ok && err != nil {
				t.Fatalf("want ok, got %v", err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalidAccountLink) {
				t.Fatalf("want ErrInvalidAccountLink, got %v", err)
			}
		})
	}
}

func TestPeopleLinkSortKeys_RoundTrip(t *testing.T) {
	k := PeopleLinkKey{MeetingID: "u1:m1", ToKind: "contact", ToID: "c1", Role: "speaker"}
	if got := PeopleLinkSortKey(k); got != "LINK#meeting#u1:m1#contact#c1#speaker" {
		t.Fatalf("forward = %q", got)
	}
	if got := PeopleLinkToSortKey(k); got != "LINKTO#contact#c1#meeting#u1:m1#speaker" {
		t.Fatalf("reverse = %q", got)
	}
	if got := PeopleLinkDenySortKey(k); got != "LINKDENY#meeting#u1:m1#contact#c1#speaker" {
		t.Fatalf("deny = %q", got)
	}
	if got, ok := ParsePeopleLinkSortKey(PeopleLinkSortKey(k)); !ok || got != k {
		t.Fatalf("forward parse = %+v %v", got, ok)
	}
	if got, ok := ParsePeopleLinkToSortKey(PeopleLinkToSortKey(k)); !ok || got != k {
		t.Fatalf("reverse parse = %+v %v", got, ok)
	}
	if !IsPeopleLinkSortKey(PeopleLinkSortKey(k)) || IsPeopleLinkSortKey("LINK#news#s:d#meeting#u1:m1") {
		t.Fatal("IsPeopleLinkSortKey mismatch")
	}
	if got := (PeopleLink{MeetingID: k.MeetingID, ToKind: k.ToKind, ToID: k.ToID, Role: k.Role}).Key(); got != k {
		t.Fatalf("Key() = %+v", got)
	}
	for _, bad := range []string{
		"LINK#meeting#u1:m1#project#p1#speaker",
		"LINK#meeting#u1:m1#contact#c1",
		"LINK#news#u1:m1#contact#c1#speaker",
		"LINKTO#contact#c1#meeting#u1:m1#speaker",
	} {
		if _, ok := ParsePeopleLinkSortKey(bad); ok {
			t.Fatalf("forward parser accepted %q", bad)
		}
	}
	if _, ok := ParsePeopleLinkToSortKey("LINKTO#contact#c1#news#u1:m1#speaker"); ok {
		t.Fatal("reverse parser accepted a non-meeting source")
	}
}

func TestParseAccountLinkSortKey_RejectsPeopleKey(t *testing.T) {
	people := PeopleLinkSortKey(PeopleLinkKey{MeetingID: "u1:m1", ToKind: "member", ToID: "x1", Role: "participant"})
	if _, ok := ParseAccountLinkSortKey(people); ok {
		t.Fatal("project 1 parser accepted a people key")
	}
	project1 := AccountLinkSortKey(AccountLinkInput{FromKind: LinkKindNews, FromID: "s1:d1", ToKind: LinkKindMeeting, ToID: "u1:m1"})
	if _, ok := ParsePeopleLinkSortKey(project1); ok {
		t.Fatal("people parser accepted a project 1 key")
	}
}

var hex32 = regexp.MustCompile(`^[0-9a-f]{32}$`)

func TestMentionHash_StableAnd32Hex(t *testing.T) {
	a, b := MentionHash("kim min"), MentionHash("kim min")
	if a != b || len(a) != 32 || !hex32.MatchString(a) {
		t.Fatalf("hash %q %q", a, b)
	}
	if MentionHash("kim min") == MentionHash("lee min") {
		t.Fatal("distinct inputs collide")
	}
	if got := MentionSortKey(a, "u1:m1", PeopleRoleAssignee); got != "MENTION#"+a+"#u1:m1#assignee" {
		t.Fatalf("mention key = %q", got)
	}
	if got := MentionIgnoreSortKey(a); got != "LINKDENY#mention#"+a {
		t.Fatalf("ignore key = %q", got)
	}
}
