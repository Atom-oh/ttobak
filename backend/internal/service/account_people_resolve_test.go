package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

func TestNormalizePersonName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ＫＩＭ  Min", "kim min"},
		{"  Lee\tJi \n Won  ", "lee ji won"},
		{"Ｐａｒｋ　ｓｏｏ", "park soo"},
		{"ﬁona", "fiona"},
		{"홍길동", "홍길동"},
	}
	for _, tc := range cases {
		if got := NormalizePersonName(tc.in); got != tc.want {
			t.Errorf("NormalizePersonName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStripHonorific(t *testing.T) {
	for _, suffix := range peopleHonorificSuffixes {
		for _, sep := range []string{"", " "} {
			got, ok := StripHonorific("홍길동" + sep + suffix)
			if !ok || got != "홍길동" {
				t.Errorf("strip %q%q = %q %v", sep, suffix, got, ok)
			}
		}
	}
	if got, ok := StripHonorific("홍길동매니저님"); !ok || got != "홍길동" {
		t.Errorf("four-syllable suffix did not win: %q %v", got, ok)
	}
	if got, ok := StripHonorific("홍길동팀장님"); !ok || got != "홍길동" {
		t.Errorf("three-syllable suffix did not win: %q %v", got, ok)
	}
	for _, bare := range []string{"님", "매니저님", " 대표"} {
		if _, ok := StripHonorific(bare); ok {
			t.Errorf("bare suffix %q stripped", bare)
		}
	}
	if got, ok := StripHonorific("홍길동 부장 님"); !ok || got != "홍길동 부장" {
		t.Errorf("stacked suffixes = %q %v, want one stripped", got, ok)
	}
	if _, ok := StripHonorific("kim min"); ok {
		t.Error("stripped a name without suffix")
	}
}

func TestHonorificSuffixesCodePoints(t *testing.T) {
	want := [][]rune{
		{0xB2D8}, {0xC528},
		{0xD300, 0xC7A5}, {0xD504, 0xB85C}, {0xCC45, 0xC784}, {0xC218, 0xC11D}, {0xC120, 0xC784},
		{0xB300, 0xB9AC}, {0xACFC, 0xC7A5}, {0xCC28, 0xC7A5}, {0xBD80, 0xC7A5}, {0xC774, 0xC0AC},
		{0xC0C1, 0xBB34}, {0xC804, 0xBB34}, {0xB300, 0xD45C},
		{0xB9E4, 0xB2C8, 0xC800}, {0xD300, 0xC7A5, 0xB2D8},
		{0xB9E4, 0xB2C8, 0xC800, 0xB2D8},
	}
	if len(peopleHonorificSuffixes) != 18 || len(want) != 18 {
		t.Fatalf("suffix count = %d", len(peopleHonorificSuffixes))
	}
	for i, s := range peopleHonorificSuffixes {
		if string(want[i]) != s {
			t.Errorf("suffix %d = %U, want %U", i, []rune(s), want[i])
		}
	}
}

func testPeopleDirectory() *PeopleDirectory {
	return &PeopleDirectory{
		People: []PersonCandidate{
			{Kind: model.LinkKindContact, ID: "c1", Name: "Kim Min", Email: "kim@x.com"},
			{Kind: model.LinkKindContact, ID: "c2", Name: "홍길동"},
			{Kind: model.LinkKindMember, ID: "u9", Name: "Lee Ji", Email: "lee@corp.com"},
		},
		Entities: []NamedCandidate{
			{Kind: model.LinkKindAccount, ID: "a2", Name: "Acme Korea", Aliases: []string{"ACMEKR"}},
			{Kind: model.LinkKindProject, ID: "p1", Name: "Migration Wave"},
		},
	}
}

func TestResolveMention_EmailRule(t *testing.T) {
	res := ResolveMention(testPeopleDirectory(), PeopleMention{Text: "Kim <KIM@x.com>", Role: model.PeopleRoleParticipant})
	if !res.Resolved || res.Kind != model.LinkKindContact || res.ID != "c1" || res.Rule != model.PeopleRuleEmail {
		t.Fatalf("got %+v", res)
	}
	dir := testPeopleDirectory()
	dir.People = append(dir.People, PersonCandidate{Kind: model.LinkKindMember, ID: "u1", Name: "Other", Email: "Kim@X.com"})
	if res := ResolveMention(dir, PeopleMention{Text: "Kim Min (kim@x.com)"}); res.Resolved {
		t.Fatalf("several email matches resolved: %+v", res)
	}
}

func TestResolveMention_NameRuleWithHonorific(t *testing.T) {
	res := ResolveMention(testPeopleDirectory(), PeopleMention{Text: "홍길동님", Role: model.PeopleRoleSpeaker})
	if !res.Resolved || res.ID != "c2" || res.Rule != model.PeopleRuleName {
		t.Fatalf("got %+v", res)
	}
	res = ResolveMention(testPeopleDirectory(), PeopleMention{Text: "unknown <nobody@x.com>"})
	if res.Resolved {
		t.Fatalf("unknown resolved: %+v", res)
	}
	// Candidate names are never stripped.
	dir := &PeopleDirectory{People: []PersonCandidate{{Kind: model.LinkKindContact, ID: "c3", Name: "박대표"}}}
	if res := ResolveMention(dir, PeopleMention{Text: "박"}); res.Resolved {
		t.Fatalf("candidate name stripped: %+v", res)
	}
}

func TestResolveMention_AmbiguousAcrossMemberAndContact(t *testing.T) {
	dir := testPeopleDirectory()
	dir.People = append(dir.People, PersonCandidate{Kind: model.LinkKindMember, ID: "u2", Name: "홍길동"})
	if res := ResolveMention(dir, PeopleMention{Text: "홍길동"}); res.Resolved {
		t.Fatalf("ambiguous resolved: %+v", res)
	}
	// One person reachable twice counts once.
	dir = testPeopleDirectory()
	dir.People = append(dir.People, PersonCandidate{Kind: model.LinkKindContact, ID: "c2", Name: "홍 길동"})
	dir.People = append(dir.People, PersonCandidate{Kind: model.LinkKindContact, ID: "c2", Name: "홍길동"})
	if res := ResolveMention(dir, PeopleMention{Text: "홍길동"}); !res.Resolved || res.ID != "c2" {
		t.Fatalf("same candidate twice: %+v", res)
	}
}

func TestResolveMention_AmbiguityStopsLaterRules(t *testing.T) {
	dir := testPeopleDirectory()
	dir.People = append(dir.People,
		PersonCandidate{Kind: model.LinkKindContact, ID: "c7", Name: "Acme Korea"},
		PersonCandidate{Kind: model.LinkKindMember, ID: "u7", Name: "Acme Korea"})
	if res := ResolveMention(dir, PeopleMention{Text: "Acme Korea", AllowEntities: true}); res.Resolved {
		t.Fatalf("later rule broke a tie: %+v", res)
	}
}

func TestResolveMention_EntitiesOnlyForInsights(t *testing.T) {
	if res := ResolveMention(testPeopleDirectory(), PeopleMention{Text: "acmekr"}); res.Resolved {
		t.Fatalf("entity resolved without AllowEntities: %+v", res)
	}
}

func TestResolveMention_AliasAndProject(t *testing.T) {
	dir := testPeopleDirectory()
	res := ResolveMention(dir, PeopleMention{Text: "ＡＣＭＥＫＲ", AllowEntities: true})
	if !res.Resolved || res.Kind != model.LinkKindAccount || res.ID != "a2" || res.Rule != model.PeopleRuleAlias {
		t.Fatalf("alias: %+v", res)
	}
	res = ResolveMention(dir, PeopleMention{Text: "migration  wave", AllowEntities: true})
	if !res.Resolved || res.Kind != model.LinkKindProject || res.ID != "p1" || res.Rule != model.PeopleRuleProject {
		t.Fatalf("project: %+v", res)
	}
	dir.Entities = append(dir.Entities, NamedCandidate{Kind: model.LinkKindProject, ID: "p2", Name: "Acme Korea"})
	if res := ResolveMention(dir, PeopleMention{Text: "Acme Korea", AllowEntities: true}); res.Resolved {
		t.Fatalf("account and project tie resolved: %+v", res)
	}
}

func TestCollectMeetingMentions_OrderAndCaps(t *testing.T) {
	m := &model.Meeting{ActionItems: "{not json", SpeakerMap: map[string]string{"spk_1": "spk_3"}}
	for i := 0; i < 60; i++ {
		m.Participants = append(m.Participants, fmt.Sprintf("p%d", i))
	}
	got, truncated := CollectMeetingMentions(m)
	if len(got) != model.MaxPeopleMentionsPerMeeting || !truncated {
		t.Fatalf("len=%d truncated=%v", len(got), truncated)
	}

	long := strings.Repeat("가", 130)
	items, _ := json.Marshal([]ActionItem{{ID: "ai1", Assignee: "홍길동"}, {ID: "ai2", Assignee: "spk_0"}, {ID: "ai3", Assignee: "spk_2"}})
	insights, _ := json.Marshal([]model.MeetingInsight{{ID: "in1", Type: "risk", Text: "budget", Entities: []string{"Acme Korea", "bad​name"}}})
	m = &model.Meeting{
		Participants: []string{"  Kim Min  ", "", long},
		SpeakerMap:   map[string]string{"spk_2": "Lee Ji", "spk_0": "홍길동", "spk_1000001": "spk_3", "spk_5": " "},
		ActionItems:  string(items),
		Insights:     string(insights),
	}
	got, truncated = CollectMeetingMentions(m)
	if truncated {
		t.Fatal("unexpected truncation")
	}
	type row struct{ text, role, ref string }
	want := []row{
		{"Kim Min", model.PeopleRoleParticipant, ""},
		{strings.Repeat("가", model.MaxPeopleMentionRunes), model.PeopleRoleParticipant, ""},
		{"홍길동", model.PeopleRoleSpeaker, "spk_0"},
		{"Lee Ji", model.PeopleRoleSpeaker, "spk_2"},
		{"홍길동", model.PeopleRoleAssignee, "ai1"},
		{"홍길동", model.PeopleRoleAssignee, "ai2"},
		{"Lee Ji", model.PeopleRoleAssignee, "ai3"},
		{"Acme Korea", model.PeopleRoleMentions, "in1"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d mentions: %+v", len(got), got)
	}
	for i, w := range want {
		if got[i].Text != w.text || got[i].Role != w.role || got[i].SourceRef != w.ref {
			t.Errorf("mention %d = %+v, want %+v", i, got[i], w)
		}
	}
	if last := got[len(got)-1]; !last.AllowEntities || last.Evidence != "risk: budget" || last.SourceField != model.PeopleSourceInsights {
		t.Errorf("insight mention = %+v", last)
	}
	if got[0].AllowEntities {
		t.Error("participant allows entities")
	}
}

func TestPlanMeetingPeople_DedupesAndCaps(t *testing.T) {
	now := time.Date(2026, 11, 9, 1, 2, 3, 0, time.UTC)
	dir := testPeopleDirectory()
	mentions := []PeopleMention{
		{Text: "Kim Min", Role: model.PeopleRoleParticipant, SourceField: model.PeopleSourceParticipants},
		{Text: "kim@x.com", Role: model.PeopleRoleParticipant, SourceField: model.PeopleSourceParticipants},
		{Text: "Kim Min", Role: model.PeopleRoleSpeaker, SourceField: model.PeopleSourceSpeakerMap, SourceRef: "spk_0"},
		{Text: "Nobody", Role: model.PeopleRoleParticipant, SourceField: model.PeopleSourceParticipants},
		{Text: "nobody", Role: model.PeopleRoleParticipant, SourceField: model.PeopleSourceParticipants},
		{Text: "Nobody", Role: model.PeopleRoleAssignee, SourceField: model.PeopleSourceActionItems, SourceRef: "ai1"},
	}
	edges, unresolved, truncated := PlanMeetingPeople("a1", "u1:m1", dir, mentions, now)
	if truncated || len(edges) != 2 || len(unresolved) != 2 {
		t.Fatalf("edges=%+v unresolved=%+v truncated=%v", edges, unresolved, truncated)
	}
	e := edges[0]
	if e.Key() != (model.PeopleLinkKey{MeetingID: "u1:m1", ToKind: model.LinkKindContact, ToID: "c1", Role: model.PeopleRoleParticipant}) ||
		e.Rule != model.PeopleRuleName || e.Mention != "Kim Min" || e.Origin != model.LinkOriginAuto ||
		e.CreatedAt != "2026-11-09T01:02:03Z" || e.AccountID != "a1" {
		t.Fatalf("first edge = %+v", e)
	}
	if edges[1].Role != model.PeopleRoleSpeaker || edges[1].SourceRef != "spk_0" {
		t.Fatalf("speaker edge = %+v", edges[1])
	}
	u := unresolved[0]
	if u.Hash != model.MentionHash("nobody") || u.Mention != "Nobody" || u.MeetingID != "u1:m1" || u.Role != model.PeopleRoleParticipant {
		t.Fatalf("unresolved = %+v", u)
	}

	dir = &PeopleDirectory{}
	mentions = nil
	for i := 0; i < 120; i++ {
		dir.People = append(dir.People, PersonCandidate{Kind: model.LinkKindContact, ID: fmt.Sprintf("c%d", i), Name: fmt.Sprintf("person %d", i)})
		mentions = append(mentions, PeopleMention{Text: fmt.Sprintf("person %d", i), Role: model.PeopleRoleParticipant})
	}
	edges, _, truncated = PlanMeetingPeople("a1", "u1:m1", dir, mentions, now)
	if len(edges) != model.MaxPeopleEdgesPerMeeting || !truncated {
		t.Fatalf("cap: len=%d truncated=%v", len(edges), truncated)
	}
}

func TestResolveMention_TruncatedDirectoryBindsByEmailOnly(t *testing.T) {
	dir := &PeopleDirectory{ContactsTruncated: true, People: []PersonCandidate{
		{Kind: model.LinkKindContact, ID: "c1", Name: "Kim Minsu", Email: "kim@acme.example"},
	}, Entities: []NamedCandidate{{Kind: model.LinkKindProject, ID: "p1", Name: "Kim Minsu"}}}
	if res := ResolveMention(dir, PeopleMention{Text: "Kim Minsu", AllowEntities: true}); res.Resolved {
		t.Fatalf("name resolved on a truncated directory: %+v", res)
	}
	if res := ResolveMention(dir, PeopleMention{Text: "kim@acme.example"}); !res.Resolved || res.Rule != model.PeopleRuleEmail {
		t.Fatalf("email: %+v", res)
	}
}
