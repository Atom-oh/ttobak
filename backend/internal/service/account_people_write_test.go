package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// PutPeopleLink and DeletePeopleLink are the member-actor writes; runner
// writes go through the fenced *InRun methods.
func (f *fakePeopleRepo) PutPeopleLink(_ context.Context, l *model.PeopleLink, actor string) error {
	if actor == "" || l.Origin != model.LinkOriginManual || l.CreatedBy != actor {
		return fmt.Errorf("manual writes need the member actor")
	}
	f.putActors = append(f.putActors, actor)
	if err := f.putErr[l.Key()]; err != nil {
		return err
	}
	if prev := f.stored[l.Key()]; prev != nil && prev.Origin == model.LinkOriginManual {
		return fmt.Errorf("put: %w", repository.ErrAccountLinkExists)
	}
	f.puts = append(f.puts, *l)
	if f.stored != nil {
		stored := *l
		f.stored[l.Key()] = &stored
		f.existing = append(f.existing, stored)
	}
	return nil
}
func (f *fakePeopleRepo) AddPeopleLinkAlias(_ context.Context, _, _ string, k model.PeopleLinkKey, hash string) error {
	l := f.stored[k]
	if l == nil || l.Origin != model.LinkOriginManual {
		return fmt.Errorf("%w: alias", repository.ErrConditionFailed)
	}
	l.MentionAliases = append(l.MentionAliases, hash)
	for i := range f.existing {
		if f.existing[i].Key() == k {
			f.existing[i].MentionAliases = l.MentionAliases
		}
	}
	return nil
}
func (f *fakePeopleRepo) DeletePeopleLink(_ context.Context, _, actor string, k model.PeopleLinkKey, origin string, deny bool) error {
	f.deletes = append(f.deletes, fmt.Sprintf("%s/%s/%s/actor=%q/%s/deny=%t", k.Role, k.ToKind, k.ToID, actor, origin, deny))
	return nil
}
func (f *fakePeopleRepo) GetPeopleLink(_ context.Context, _ string, k model.PeopleLinkKey) (*model.PeopleLink, error) {
	return f.stored[k], nil
}
func (f *fakePeopleRepo) ListUnresolvedMentionsByHash(_ context.Context, _, hash string, maxRows int) ([]model.UnresolvedMention, bool, error) {
	var rows []model.UnresolvedMention
	for _, m := range f.mentionRows {
		if m.Hash == hash {
			rows = append(rows, m)
		}
	}
	if len(rows) > maxRows {
		return rows[:maxRows], true, nil
	}
	return rows, false, nil
}
func (f *fakePeopleRepo) DeleteUnresolvedMention(_ context.Context, _, hash, meetingID, role, _ string) error {
	if f.staleMentions[meetingID] {
		return fmt.Errorf("%w: rewritten", repository.ErrConditionFailed)
	}
	f.mentionDeletes = append(f.mentionDeletes, hash+"/"+meetingID+"/"+role)
	return nil
}
func (f *fakePeopleRepo) PutMentionIgnore(_ context.Context, _, actor, hash string) error {
	f.ignorePuts = append(f.ignorePuts, actor+"/"+hash)
	return nil
}

func peopleWriteRepo() *fakePeopleRepo {
	f := newPeopleReadRepo()
	f.meetings["owner:m1"].SpeakerMap = map[string]string{"spk_0": " Kim Minsu "}
	f.meetings["owner:m3"] = peopleTestMeeting("m3")
	f.contacts = []model.AccountContact{peopleContact(peopleTestContact, "Kim Minsu", "")}
	f.stored = map[model.PeopleLinkKey]*model.PeopleLink{}
	return f
}

func speakerInput(ref string) model.PeopleEdgeInput {
	return model.PeopleEdgeInput{MeetingID: "owner:m1", ToKind: "contact", ToID: peopleTestContact, Role: "speaker", SourceRef: ref}
}

func TestBindEdge_SpeakerRequiresKnownLabel(t *testing.T) {
	f := peopleWriteRepo()
	s := newPeopleTestService(f)
	for _, ref := range []string{"", "Bob", "spk_x"} {
		if _, err := s.BindEdge(context.Background(), "viewer", "acc", speakerInput(ref)); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("ref %q err=%v", ref, err)
		}
	}
	link, err := s.BindEdge(context.Background(), "viewer", "acc", speakerInput("spk_0"))
	if err != nil || link.Mention != "Kim Minsu" || link.SourceField != model.PeopleSourceSpeakerMap || link.SourceRef != "spk_0" {
		t.Fatalf("link=%+v err=%v", link, err)
	}
	// An unmapped default label of the meeting is accepted with no mention.
	unmapped := speakerInput("spk_1")
	unmapped.ToID = peopleTestContact2
	if link, err := s.BindEdge(context.Background(), "viewer", "acc", unmapped); err != nil || link.Mention != "" {
		t.Fatalf("default label link=%+v err=%v", link, err)
	}
	if len(f.puts) != 2 {
		t.Fatalf("puts %+v", f.puts)
	}
}

func TestBindEdge_NonMemberForbidden(t *testing.T) {
	f := peopleWriteRepo()
	if _, err := newPeopleTestService(f).BindEdge(context.Background(), "stranger", "acc", speakerInput("spk_0")); !errors.Is(err, ErrForbidden) || len(f.puts) != 0 {
		t.Fatalf("err=%v puts=%d", err, len(f.puts))
	}
	in := speakerInput("spk_0")
	in.MeetingID = "owner:m2"
	if _, err := newPeopleTestService(f).BindEdge(context.Background(), "viewer", "acc", in); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unpublished err=%v", err)
	}
}

func TestBindEdge_ReplacesAutoEdge(t *testing.T) {
	f := peopleWriteRepo()
	in := model.PeopleEdgeInput{MeetingID: "owner:m1", ToKind: "contact", ToID: peopleTestContact, Role: "participant"}
	auto := peopleEdge("owner:m1", "contact", peopleTestContact, "participant", model.PeopleSourceParticipants)
	f.stored[auto.Key()] = &auto
	link, err := newPeopleTestService(f).BindEdge(context.Background(), "viewer", "acc", in)
	if err != nil || len(f.puts) != 1 || f.puts[0].Origin != model.LinkOriginManual || f.puts[0].CreatedBy != "viewer" ||
		f.putActors[0] != "viewer" || link.SourceField != model.PeopleSourceParticipants {
		t.Fatalf("link=%+v puts=%+v err=%v", link, f.puts, err)
	}
}

func TestBindEdge_IdempotentOnManual(t *testing.T) {
	f := peopleWriteRepo()
	in := model.PeopleEdgeInput{MeetingID: "owner:m1", ToKind: "contact", ToID: peopleTestContact, Role: "participant"}
	manual := peopleEdge("owner:m1", "contact", peopleTestContact, "participant", model.PeopleSourceParticipants)
	manual.Origin, manual.CreatedBy = model.LinkOriginManual, "owner"
	f.stored[manual.Key()] = &manual
	f.putErr[manual.Key()] = fmt.Errorf("put: %w", repository.ErrAccountLinkExists)
	link, err := newPeopleTestService(f).BindEdge(context.Background(), "viewer", "acc", in)
	if err != nil || link.CreatedBy != "owner" {
		t.Fatalf("link=%+v err=%v", link, err)
	}
	f.putErr[manual.Key()] = fmt.Errorf("put: %w", repository.ErrAccountLinkTarget)
	if _, err := newPeopleTestService(f).BindEdge(context.Background(), "viewer", "acc", in); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("target err=%v", err)
	}
}

func TestUnbindEdge_AutoWritesDeny(t *testing.T) {
	f := peopleWriteRepo()
	auto := peopleEdge("owner:m1", "contact", peopleTestContact, "speaker", model.PeopleSourceSpeakerMap)
	f.stored[auto.Key()] = &auto
	if err := newPeopleTestService(f).UnbindEdge(context.Background(), "viewer", "acc", auto.Key()); err != nil ||
		len(f.deletes) != 1 || f.deletes[0] != `speaker/contact/`+peopleTestContact+`/actor="viewer"/auto/deny=true` {
		t.Fatalf("deletes=%v err=%v", f.deletes, err)
	}
}

func TestUnbindEdge_ManualNoDeny(t *testing.T) {
	f := peopleWriteRepo()
	manual := peopleEdge("owner:m1", "contact", peopleTestContact, "speaker", model.PeopleSourceSpeakerMap)
	manual.Origin = model.LinkOriginManual
	f.stored[manual.Key()] = &manual
	s := newPeopleTestService(f)
	if err := s.UnbindEdge(context.Background(), "viewer", "acc", manual.Key()); err != nil || len(f.deletes) != 1 || !strings.HasSuffix(f.deletes[0], "/manual/deny=false") {
		t.Fatalf("deletes=%v err=%v", f.deletes, err)
	}
	missing := manual.Key()
	missing.Role = "participant"
	if err := s.UnbindEdge(context.Background(), "viewer", "acc", missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing err=%v", err)
	}
}

func TestResolveMentionGroup_WritesManualPerVisibleMeeting(t *testing.T) {
	f := peopleWriteRepo()
	f.mentionRows = []model.UnresolvedMention{
		peopleMentionRow(peopleHashA, "owner:m1", "participant", model.PeopleSourceParticipants, "Minsu"),
		peopleMentionRow(peopleHashA, "owner:m2", "participant", model.PeopleSourceParticipants, "Minsu"),
		peopleMentionRow(peopleHashA, "owner:m3", "speaker", model.PeopleSourceSpeakerMap, "Minsu"),
	}
	f.mentionRows[2].SourceRef = "spk_0"
	res, err := newPeopleTestService(f).ResolveMentionGroup(context.Background(), "viewer", "acc", peopleHashA,
		model.ResolveMentionInput{Kind: "contact", ID: peopleTestContact})
	if err != nil || res.Written != 2 || res.Remaining || len(f.mentionDeletes) != 2 {
		t.Fatalf("res=%+v deletes=%v err=%v", res, f.mentionDeletes, err)
	}
	for _, l := range f.puts {
		if l.Origin != model.LinkOriginManual || l.Rule != "" || l.Mention != "Minsu" || l.CreatedBy != "viewer" || l.MeetingID == "owner:m2" {
			t.Fatalf("edge %+v", l)
		}
	}
	if f.puts[1].Role != "speaker" || f.puts[1].SourceRef != "spk_0" || f.puts[1].SourceField != model.PeopleSourceSpeakerMap {
		t.Fatalf("speaker edge %+v", f.puts[1])
	}
}

func TestResolveMentionGroup_RejectsProjectKind(t *testing.T) {
	f := peopleWriteRepo()
	s := newPeopleTestService(f)
	for _, in := range []model.ResolveMentionInput{{Kind: "project", ID: "p1"}, {Kind: "account", ID: "sub1"}, {Kind: "contact", ID: peopleTestContact2}} {
		if _, err := s.ResolveMentionGroup(context.Background(), "viewer", "acc", peopleHashA, in); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("in %+v err=%v", in, err)
		}
	}
}

func TestIgnoreMentionGroup_WritesIgnoreAndDeletesRows(t *testing.T) {
	f := peopleWriteRepo()
	f.mentionRows = []model.UnresolvedMention{
		peopleMentionRow(peopleHashA, "owner:m1", "participant", model.PeopleSourceParticipants, "Minsu"),
		peopleMentionRow(peopleHashA, "owner:m2", "participant", model.PeopleSourceParticipants, "Minsu"),
	}
	if err := newPeopleTestService(f).IgnoreMentionGroup(context.Background(), "viewer", "acc", peopleHashA); err != nil ||
		len(f.ignorePuts) != 1 || f.ignorePuts[0] != "viewer/"+peopleHashA ||
		len(f.mentionDeletes) != 1 || f.mentionDeletes[0] != peopleHashA+"/owner:m1/participant" {
		t.Fatalf("ignores=%v deletes=%v err=%v", f.ignorePuts, f.mentionDeletes, err)
	}
}

// Two texts of one person in the same meeting and role, resolved to the
// same contact, both stay resolved across resolver runs.
func TestResolveMentionGroup_SecondVariantSurvivesRerun(t *testing.T) {
	f := peopleWriteRepo()
	f.contacts = []model.AccountContact{peopleContact(peopleTestContact, "Park Jiwon", "")}
	f.meetings["owner:m1"].Participants = []string{"Jiwon", "Jiwon P."}
	f.meetings["owner:m1"].SpeakerMap = nil
	var events []model.PeopleResolveRequested
	s := newPeopleTestService(f)
	s.SetPeopleResolvePublisher(func(_ context.Context, e model.PeopleResolveRequested) error { events = append(events, e); return nil })
	if _, err := s.ResolveMeeting(context.Background(), "owner", "m1", PeopleRunOptions{}); err != nil || len(f.replaced[0]) != 2 {
		t.Fatalf("first run unresolved=%+v err=%v", f.replaced, err)
	}
	f.mentionRows = f.replaced[0]
	target := model.ResolveMentionInput{Kind: "contact", ID: peopleTestContact}
	for _, row := range f.replaced[0] {
		if res, err := s.ResolveMentionGroup(context.Background(), "viewer", "acc", row.Hash, target); err != nil || res.Written != 1 {
			t.Fatalf("resolve %q: res=%+v err=%v", row.Mention, res, err)
		}
	}
	if len(f.puts) != 1 || len(f.stored) != 1 || len(f.existing[0].MentionAliases) != 1 || len(f.mentionDeletes) != 2 {
		t.Fatalf("puts=%+v existing=%+v deletes=%v", f.puts, f.existing, f.mentionDeletes)
	}
	if len(events) != 2 || events[0] != (model.PeopleResolveRequested{AccountID: "acc", OwnerID: "owner", MeetingID: "m1", Reason: model.PeopleResolveReasonManual}) {
		t.Fatalf("refresh events %+v", events)
	}
	f.prior = nil
	if _, err := s.ResolveMeeting(context.Background(), "owner", "m1", PeopleRunOptions{}); err != nil {
		t.Fatal(err)
	}
	if last := f.replaced[len(f.replaced)-1]; len(last) != 0 {
		t.Fatalf("rerun recreated unresolved mentions %+v", last)
	}
}

func TestResolveMentionGroup_RefreshesStaleMeetingsWhenCoalescing(t *testing.T) {
	f := peopleWriteRepo()
	f.staleMentions = map[string]bool{"owner:m4": true}
	for i := 1; i <= 6; i++ {
		id := fmt.Sprint("m", i)
		f.meetings["owner:"+id] = peopleTestMeeting(id)
		f.mentionRows = append(f.mentionRows, peopleMentionRow(peopleHashA, "owner:"+id, "participant", model.PeopleSourceParticipants, "Minsu"))
	}
	var events []model.PeopleResolveRequested
	s := newPeopleTestService(f)
	s.SetPeopleResolvePublisher(func(_ context.Context, e model.PeopleResolveRequested) error { events = append(events, e); return nil })
	res, err := s.ResolveMentionGroup(context.Background(), "viewer", "acc", peopleHashA, model.ResolveMentionInput{Kind: "contact", ID: peopleTestContact})
	if err != nil || res.Written != 6 || len(f.mentionDeletes) != 5 {
		t.Fatalf("res=%+v deletes=%v err=%v", res, f.mentionDeletes, err)
	}
	want := []model.PeopleResolveRequested{{AccountID: "acc", Reason: model.PeopleResolveReasonManual},
		{AccountID: "acc", OwnerID: "owner", MeetingID: "m4", Reason: model.PeopleResolveReasonManual}}
	if fmt.Sprint(events) != fmt.Sprint(want) {
		t.Fatalf("events %+v", events)
	}
}
