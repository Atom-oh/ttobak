package service

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

func TestPeopleCandidates_InsightEntitiesAreNotAllPeople(t *testing.T) {
	meeting := &model.Meeting{
		Participants: []string{"Unknown Participant"},
		Insights: `[{"id":"in1","type":"tech","text":"migration",
			"entities":["SKT","UI/UX","디자인 시스템","팀 구성원","Kim Min","Acme Korea","Migration Wave","홍테스트"],
			"personEntities":["홍테스트"]}]`,
	}
	mentions, _ := CollectMeetingMentions(meeting)
	edges, unresolved, _ := PlanMeetingPeople("acc", "owner:m1", testPeopleDirectory(), mentions, time.Time{})
	if len(edges) != 3 {
		t.Fatalf("known people, accounts and projects must still resolve: %+v", edges)
	}
	var texts []string
	for _, row := range unresolved {
		if !row.PersonCandidate {
			t.Fatal("new review rows must carry their person classification")
		}
		texts = append(texts, row.Mention)
	}
	slices.Sort(texts)
	if !slices.Equal(texts, []string{"Unknown Participant", "홍테스트"}) {
		t.Fatalf("only explicit person candidates belong in the review queue: %v", texts)
	}
}

func TestPeopleCandidates_TypedNamesReachReviewAndManualResolution(t *testing.T) {
	f := peopleWriteRepo()
	meeting := f.meetings["owner:m1"]
	meeting.Insights = `[{"id":"in1","type":"stakeholder","text":"joined",
		"entities":["홍테스트"],"personEntities":["홍테스트"]}]`
	mentions, _ := CollectMeetingMentions(meeting)
	_, f.mentionRows, _ = PlanMeetingPeople("acc", "owner:m1", &PeopleDirectory{}, mentions, time.Time{})
	s := newPeopleTestService(f)
	page, err := s.ListMentionGroups(context.Background(), "viewer", "acc", "")
	if err != nil || len(page.Groups) != 2 {
		t.Fatalf("typed person plus the mapped speaker must remain reviewable: %+v %v", page, err)
	}
	result, err := s.ResolveMentionGroup(context.Background(), "viewer", "acc", model.MentionHash(NormalizePersonName("홍테스트")),
		model.ResolveMentionInput{Kind: "contact", ID: peopleTestContact})
	if err != nil || result.Written != 1 || len(f.puts) != 1 || f.puts[0].Mention != "홍테스트" {
		t.Fatalf("typed person must still support manual binding: result=%+v err=%v", result, err)
	}
}

func TestExtractInsights_ClassifiedPeopleAreBoundedAndGrounded(t *testing.T) {
	source := "Ada Lovelace와 홍테스트가 PoC 일정을 검토했다."
	meeting := &model.Meeting{MeetingID: "m1", UserID: "owner", Content: source}
	response := `[{"type":"stakeholder","text":"일정 검토","entities":["Ada Lovelace","홍테스트","Not In Source","spk_5, spk_9","PoC"],
		"personEntities":["Ada Lovelace","홍테스트","Not In Source","Missing From Entities","spk_5, spk_9","홍테스트"]}]`
	request, raw, writes, err := invokeNoteSourceFixture(t, meeting, actionItemsModelResponse(t, response, "end_turn"),
		func(s *BedrockService) (string, error) {
			return s.ExtractInsightsForMeeting(context.Background(), meeting)
		})
	var got []model.MeetingInsight
	if err != nil || json.Unmarshal([]byte(raw), &got) != nil || len(got) != 1 ||
		!slices.Equal(got[0].PersonEntities, []string{"Ada Lovelace", "홍테스트"}) || writes != 0 {
		t.Fatalf("only grounded, distinct names from the entity set survive: output=%s writes=%d err=%v", raw, writes, err)
	}
	if !strings.Contains(request.System, `"personEntities"`) || !strings.Contains(request.System, "회사·조직") {
		t.Fatal("the extraction request must distinguish humans from mixed entities")
	}
}

func TestPeopleCandidates_SpeakerCodeListsAreNotNames(t *testing.T) {
	meeting := &model.Meeting{
		Participants: []string{"spk_0", "spk_5, spk_9", "화자A / 화자B", "Ada Lovelace"},
		SpeakerMap: map[string]string{
			"spk_1": "spk_5, spk_9",
			"spk_2": "Kim Min",
		},
		ActionItems: `[{"id":"ai1","assignee":"spk_0, spk_7"},{"id":"ai2","assignee":"spk_2"},{"id":"ai3","assignee":"spk_1"}]`,
		Insights: `[{"id":"in1","type":"action","text":"follow up",
			"entities":["spk_5, spk_9","화자A / 화자B"],"personEntities":["spk_5, spk_9","화자A / 화자B"]}]`,
	}
	mentions, _ := CollectMeetingMentions(meeting)
	var texts []string
	for _, mention := range mentions {
		texts = append(texts, mention.Text)
	}
	if !slices.Equal(texts, []string{"Ada Lovelace", "Kim Min", "Kim Min"}) {
		t.Fatalf("unmapped diarization codes must never become names: %v", texts)
	}
}

func TestPeopleCandidates_LegacyReviewRowsAreHiddenWithoutDeletingManualEdges(t *testing.T) {
	f := newPeopleReadRepo()
	f.mentionRows = []model.UnresolvedMention{
		peopleMentionRow(peopleHashA, "owner:m1", "mentions", model.PeopleSourceInsights, "디자인 시스템"),
		peopleMentionRow(peopleHashB, "owner:m1", "assignee", model.PeopleSourceActionItems, "spk_5, spk_9"),
		peopleMentionRow(peopleHashC, "owner:m1", "participant", model.PeopleSourceParticipants, "Unknown Participant"),
	}
	f.contacts = []model.AccountContact{peopleContact(peopleTestContact, "Kim Min", "")}
	manual := peopleEdge("owner:m1", "contact", peopleTestContact, "mentions", model.PeopleSourceInsights)
	manual.Origin = model.LinkOriginManual
	f.existing = []model.PeopleLink{manual}
	s := newPeopleTestService(f)
	page, err := s.ListMentionGroups(context.Background(), "viewer", "acc", "")
	if err != nil || len(page.Groups) != 1 || page.Groups[0].Text != "Unknown Participant" {
		t.Fatalf("legacy mixed entities and speaker lists must disappear immediately: page=%+v err=%v", page, err)
	}
	result, err := s.ResolveMentionGroup(context.Background(), "viewer", "acc", peopleHashA,
		model.ResolveMentionInput{Kind: "contact", ID: peopleTestContact})
	if err != nil || result.Written != 0 || result.Remaining || len(f.puts) != 0 {
		t.Fatalf("a stale review page must not bind a non-person: result=%+v err=%v", result, err)
	}
	edges, err := s.MeetingEdges(context.Background(), "viewer", "acc", "owner:m1")
	if err != nil || len(edges.Edges) != 1 || edges.Edges[0].Origin != model.LinkOriginManual {
		t.Fatalf("manual decisions must remain intact: edges=%+v err=%v", edges, err)
	}
}

func TestPeopleCandidates_TypedNamesSurviveManyGenericEntities(t *testing.T) {
	insights := make([]map[string]any, 0, 61)
	for range 60 {
		insights = append(insights, map[string]any{"id": "term", "type": "tech", "text": "topic", "entities": []string{"UI/UX"}})
	}
	insights = append(insights, map[string]any{"id": "person", "type": "stakeholder", "text": "joined",
		"entities": []string{"홍테스트"}, "personEntities": []string{"홍테스트"}})
	raw, _ := json.Marshal(insights)
	mentions, _ := CollectMeetingMentions(&model.Meeting{Insights: string(raw)})
	_, unresolved, _ := PlanMeetingPeople("acc", "owner:m1", &PeopleDirectory{}, mentions, time.Time{})
	if len(unresolved) != 1 || unresolved[0].Mention != "홍테스트" {
		t.Fatalf("generic entity volume must not starve person candidates: %+v", unresolved)
	}
}
