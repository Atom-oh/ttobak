package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

func peopleMember(account, user, email string) model.AccountMember {
	return model.AccountMember{PK: "ACCOUNT#" + account, SK: "MEMBER#" + user, AccountID: account, UserID: user, Email: email,
		EntityType: model.EntityTypeAccountMember}
}

func (f *fakePeopleRepo) GetMember(_ context.Context, account, user string) (*model.AccountMember, error) {
	for i := range f.members {
		if m := f.members[i]; m.AccountID == account && m.UserID == user {
			return &m, nil
		}
	}
	return nil, nil
}
func (f *fakePeopleRepo) GetAccountContact(_ context.Context, _, id string) (*model.AccountContact, error) {
	if err := f.contactErr[id]; err != nil {
		return nil, err
	}
	for i := range f.contacts {
		if c := f.contacts[i]; c.ContactID == id {
			return &c, nil
		}
	}
	return nil, nil
}
func (f *fakePeopleRepo) GetMeetingPublication(_ context.Context, owner, id string) (*model.Meeting, error) {
	f.pubReads++
	m := f.meetings[owner+":"+id]
	if m == nil {
		return nil, nil
	}
	c := *m
	c.PK, c.SK = model.PrefixUser+owner, model.PrefixMeeting+id
	return &c, nil
}
func (f *fakePeopleRepo) ListPeopleLinksByTarget(_ context.Context, _, kind, after string, maxRows int) ([]model.PeopleLink, string, error) {
	var rows []model.PeopleLink
	for _, l := range f.targetLinks {
		if sk := model.PeopleLinkToSortKey(l.Key()); l.ToKind == kind && sk > after {
			rows = append(rows, l)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		return model.PeopleLinkToSortKey(rows[i].Key()) < model.PeopleLinkToSortKey(rows[j].Key())
	})
	if len(rows) > maxRows {
		rows = rows[:maxRows]
		return rows, model.PeopleLinkToSortKey(rows[maxRows-1].Key()), nil
	}
	return rows, "", nil
}
func (f *fakePeopleRepo) ListUnresolvedMentions(_ context.Context, _, after string, maxRows int) ([]model.UnresolvedMention, string, error) {
	var rows []model.UnresolvedMention
	sk := func(m model.UnresolvedMention) string { return model.MentionSortKey(m.Hash, m.MeetingID, m.Role) }
	for _, m := range f.mentionRows {
		if sk(m) > after {
			rows = append(rows, m)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return sk(rows[i]) < sk(rows[j]) })
	if len(rows) > maxRows {
		rows = rows[:maxRows]
		return rows, sk(rows[maxRows-1]), nil
	}
	return rows, "", nil
}

func newPeopleReadRepo() *fakePeopleRepo {
	f := newFakePeopleRepo()
	f.members = []model.AccountMember{peopleMember("acc", "viewer", "viewer@x.example"), peopleMember("acc", "owner", "owner@x.example")}
	f.meetings["owner:m2"] = &model.Meeting{MeetingID: "m2", UserID: "owner", AccountID: "acc", SharedToAccount: false, EntityType: "MEETING"}
	return f
}

func peopleEdge(meeting, kind, id, role, field string) model.PeopleLink {
	return model.PeopleLink{AccountID: "acc", MeetingID: meeting, ToKind: kind, ToID: id, Role: role, Origin: model.LinkOriginAuto,
		SourceField: field, CreatedAt: "2026-11-09T00:00:00Z"}
}

func withPeopleSummaryGuard(t *testing.T) {
	prev := accountMemberNeedsSummaryGuard
	accountMemberNeedsSummaryGuard = func(*model.AccountMember) bool { return true }
	t.Cleanup(func() { accountMemberNeedsSummaryGuard = prev })
}

func TestListPeople_CountsOnlyPublishedMeetings(t *testing.T) {
	f := newPeopleReadRepo()
	f.contacts = []model.AccountContact{peopleContact(peopleTestContact, "Kim Minsu", "kim@acme.example")}
	f.targetLinks = []model.PeopleLink{
		peopleEdge("owner:m1", "contact", peopleTestContact, "speaker", model.PeopleSourceSpeakerMap),
		peopleEdge("owner:m1", "contact", peopleTestContact, "participant", model.PeopleSourceParticipants),
		peopleEdge("owner:m2", "contact", peopleTestContact, "participant", model.PeopleSourceParticipants),
	}
	page, err := newPeopleTestService(f).ListPeople(context.Background(), "viewer", "acc", "")
	if err != nil || len(page.People) != 1 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	p := page.People[0]
	if p.Kind != "contact" || p.Label != "Kim Minsu" || p.Meetings != 1 || p.Roles["speaker"] != 1 || p.Roles["participant"] != 1 || page.NextCursor != "" {
		t.Fatalf("person %+v page %+v", p, page)
	}
}

func TestListPeople_RemovedMemberForbidden(t *testing.T) {
	f := newPeopleReadRepo()
	if _, err := newPeopleTestService(f).ListPeople(context.Background(), "stranger", "acc", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("err=%v", err)
	}
}

func TestListPeople_SkipsDeletedContactAndFormerMember(t *testing.T) {
	f := newPeopleReadRepo()
	f.users["owner"] = &model.User{UserID: "owner", Name: "Owner Kim"}
	f.contacts = []model.AccountContact{peopleContact(peopleTestContact, "Kim", "")}
	f.contactErr["c-sealed"] = fmt.Errorf("open: %w", repository.ErrContactUnreadable)
	f.targetLinks = []model.PeopleLink{
		peopleEdge("owner:m1", "contact", peopleTestContact, "speaker", model.PeopleSourceSpeakerMap),
		peopleEdge("owner:m1", "contact", peopleTestContact2, "speaker", model.PeopleSourceSpeakerMap),
		peopleEdge("owner:m1", "contact", "c-sealed", "speaker", model.PeopleSourceSpeakerMap),
		peopleEdge("owner:m1", "member", "owner", "speaker", model.PeopleSourceSpeakerMap),
		peopleEdge("owner:m1", "member", "gone", "speaker", model.PeopleSourceSpeakerMap),
	}
	page, err := newPeopleTestService(f).ListPeople(context.Background(), "viewer", "acc", "")
	if err != nil || len(page.People) != 2 || page.People[0].Label != "Owner Kim" || page.People[1].ID != peopleTestContact {
		t.Fatalf("page=%+v err=%v", page, err)
	}
}

func TestListPeople_PageBoundaryKeepsPersonWhole(t *testing.T) {
	f := newPeopleReadRepo()
	var ids []string
	for i := 0; i < 201; i++ {
		id := fmt.Sprintf("%08d-0000-4000-8000-000000000000", i)
		ids = append(ids, id)
		f.contacts = append(f.contacts, peopleContact(id, fmt.Sprint("P", i), ""))
		f.targetLinks = append(f.targetLinks, peopleEdge("owner:m1", "contact", id, "speaker", model.PeopleSourceSpeakerMap))
	}
	f.targetLinks = append(f.targetLinks, peopleEdge("owner:m1", "contact", ids[200], "participant", model.PeopleSourceParticipants))
	s := newPeopleTestService(f)
	page, err := s.ListPeople(context.Background(), "viewer", "acc", "")
	if err != nil || len(page.People) != 200 || page.NextCursor == "" || page.People[199].ID != ids[199] {
		t.Fatalf("page 1: n=%d next=%q err=%v", len(page.People), page.NextCursor, err)
	}
	page2, err := s.ListPeople(context.Background(), "viewer", "acc", page.NextCursor)
	if err != nil || len(page2.People) != 1 || page2.NextCursor != "" {
		t.Fatalf("page 2: %+v err=%v", page2, err)
	}
	if p := page2.People[0]; p.ID != ids[200] || p.Meetings != 1 || p.Roles["speaker"] != 1 || p.Roles["participant"] != 1 {
		t.Fatalf("person 201 %+v", p)
	}
}

func TestListPeople_RejectsForeignCursor(t *testing.T) {
	s := newPeopleTestService(newPeopleReadRepo())
	for _, c := range []string{
		encodePeopleCursor("other", "member", ""),
		encodePeopleCursor("acc", "member", "LINKTO#contact#x"),
		encodePeopleCursor("acc", "mention", ""),
		strings.Repeat("a", maxPeopleCursorBytes+1),
		"!!",
	} {
		if _, err := s.ListPeople(context.Background(), "viewer", "acc", c); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("cursor %q err=%v", c, err)
		}
	}
}

func peopleMentionRow(hash, meeting, role, field, text string) model.UnresolvedMention {
	return model.UnresolvedMention{AccountID: "acc", Hash: hash, MeetingID: meeting, Role: role, SourceField: field, Mention: text, CreatedAt: "t"}
}

const peopleHashA = "0123456789abcdef0123456789abcdef"
const peopleHashB = "fedcba9876543210fedcba9876543210"
const peopleHashC = "ffffffffffffffffffffffffffffffff"

func TestPeopleMentions_HidesSummaryDerivedTextUnderGuard(t *testing.T) {
	withPeopleSummaryGuard(t)
	f := newPeopleReadRepo()
	f.mentionRows = []model.UnresolvedMention{
		peopleMentionRow(peopleHashA, "owner:m1", "mentions", model.PeopleSourceInsights, "Kim Minsu"),
		peopleMentionRow(peopleHashB, "owner:m1", "participant", model.PeopleSourceParticipants, "Lee"),
	}
	f.mentionRows[0].PersonCandidate = true
	s := newPeopleTestService(f)
	page, err := s.ListMentionGroups(context.Background(), "viewer", "acc", "")
	if err != nil || len(page.Groups) != 1 || page.Groups[0].Key != peopleHashB || page.Groups[0].Text != "Lee" {
		t.Fatalf("guarded page=%+v err=%v", page, err)
	}
	page, err = s.ListMentionGroups(context.Background(), "owner", "acc", "")
	if err != nil || len(page.Groups) != 2 || page.Groups[0].Text != "Kim Minsu" || page.Groups[0].Roles[0] != "mentions" {
		t.Fatalf("owner page=%+v err=%v", page, err)
	}
}

func TestPeopleMentions_UnpublishedMeetingNotCounted(t *testing.T) {
	f := newPeopleReadRepo()
	f.mentionRows = []model.UnresolvedMention{
		peopleMentionRow(peopleHashA, "owner:m1", "participant", model.PeopleSourceParticipants, "Lee"),
		peopleMentionRow(peopleHashA, "owner:m2", "participant", model.PeopleSourceParticipants, "Lee (hidden)"),
		peopleMentionRow(peopleHashB, "owner:m2", "speaker", model.PeopleSourceSpeakerMap, "Park"),
		peopleMentionRow(peopleHashC, "owner:m1", "speaker", model.PeopleSourceSpeakerMap, "Not a person"),
	}
	f.ignores[peopleHashC] = true
	page, err := newPeopleTestService(f).ListMentionGroups(context.Background(), "viewer", "acc", "")
	if err != nil || len(page.Groups) != 1 || page.Groups[0].Meetings != 1 || page.Groups[0].Text != "Lee" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
}

func TestPeopleEdges_BlanksEvidenceUnderGuard(t *testing.T) {
	withPeopleSummaryGuard(t)
	f := newPeopleReadRepo()
	f.contacts = []model.AccountContact{peopleContact(peopleTestContact, "Kim", "")}
	speaker := peopleEdge("owner:m1", "contact", peopleTestContact, "participant", model.PeopleSourceParticipants)
	speaker.Mention = "Kim"
	insight := peopleEdge("owner:m1", "contact", peopleTestContact, "mentions", model.PeopleSourceInsights)
	insight.Mention, insight.Evidence = "Kim", "risk: Kim leaves"
	f.existing = []model.PeopleLink{speaker, insight}
	s := newPeopleTestService(f)
	out, err := s.MeetingEdges(context.Background(), "viewer", "acc", "owner:m1")
	if err != nil || len(out.Edges) != 2 || out.Edges[0].Mention != "Kim" || out.Edges[1].Mention != "" || out.Edges[1].Evidence != "" ||
		out.Labels["contact:"+peopleTestContact] != "Kim" {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if out, _ := s.MeetingEdges(context.Background(), "owner", "acc", "owner:m1"); out.Edges[1].Evidence == "" {
		t.Fatalf("owner sees evidence: %+v", out)
	}
	if _, err := s.MeetingEdges(context.Background(), "viewer", "acc", "owner:m2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unpublished err=%v", err)
	}
}

func TestPeopleEdges_AccountLabelNeedsReadGrant(t *testing.T) {
	f := newPeopleReadRepo()
	for _, id := range []string{"sub1", "sub2"} {
		f.accounts[id] = &model.Account{PK: "ACCOUNT#" + id, SK: "META", AccountID: id, Name: "Name " + id, EntityType: model.EntityTypeAccount}
	}
	f.members = append(f.members, peopleMember("sub1", "viewer", ""))
	f.existing = []model.PeopleLink{
		peopleEdge("owner:m1", "account", "sub1", "mentions", model.PeopleSourceInsights),
		peopleEdge("owner:m1", "account", "sub2", "mentions", model.PeopleSourceInsights),
	}
	out, err := newPeopleTestService(f).MeetingEdges(context.Background(), "viewer", "acc", "owner:m1")
	if err != nil || len(out.Edges) != 1 || out.Edges[0].ToID != "sub1" || out.Labels["account:sub1"] != "Name sub1" || out.Labels["account:sub2"] != "" {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}
