package service

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"

	"github.com/ttobak/backend/internal/model"
)

// peopleHonorificSuffixes is the fixed list of trailing Korean honorific and
// title tokens that the name rule may strip once from a mention (spec E3).
var peopleHonorificSuffixes = []string{
	"님", "씨",
	"팀장", "프로", "책임", "수석", "선임", "대리", "과장", "차장", "부장", "이사", "상무", "전무", "대표",
	"매니저", "팀장님",
	"매니저님",
}

// peopleHonorificsLongestFirst orders the suffixes so a longer token wins
// over a shorter one that it ends with.
var peopleHonorificsLongestFirst = func() []string {
	out := append([]string(nil), peopleHonorificSuffixes...)
	sort.SliceStable(out, func(i, j int) bool {
		return utf8.RuneCountInString(out[i]) > utf8.RuneCountInString(out[j])
	})
	return out
}()

var peopleEmailPattern = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)

// NormalizePersonName applies NFKC, trims, collapses whitespace runs to one
// space and case-folds.
func NormalizePersonName(s string) string {
	s = strings.Join(strings.Fields(norm.NFKC.String(s)), " ")
	return cases.Fold().String(s)
}

// StripHonorific strips one trailing suffix, with or without a preceding
// space. ok is false when nothing was stripped or the remainder would be empty.
func StripHonorific(normalized string) (string, bool) {
	for _, suffix := range peopleHonorificsLongestFirst {
		if !strings.HasSuffix(normalized, suffix) {
			continue
		}
		rest := strings.TrimRight(strings.TrimSuffix(normalized, suffix), " ")
		if rest == "" {
			return "", false
		}
		return rest, true
	}
	return "", false
}

// PersonCandidate is a member or contact the resolver may bind a mention to.
type PersonCandidate struct {
	Kind  string // member|contact
	ID    string
	Name  string
	Email string
}

// NamedCandidate is a descendant account or linked project.
type NamedCandidate struct {
	Kind    string // account|project
	ID      string
	Name    string
	Aliases []string
}

// PeopleDirectory is the candidate set for one account.
type PeopleDirectory struct {
	People            []PersonCandidate
	Entities          []NamedCandidate
	ContactsTruncated bool
}

// PeopleMention is one free-text mention taken from a meeting field.
type PeopleMention struct {
	Text, Role, SourceField, SourceRef, Evidence string
	AllowEntities                                bool // insight entities only (rule 3)
}

// MentionResolution is the outcome of ResolveMention. Normalized is the
// normalized mention text used for grouping and hashing.
type MentionResolution struct {
	Kind, ID, Rule string
	Resolved       bool
	Normalized     string
}

type candidateKey struct{ kind, id string }

// candidateSet counts distinct (Kind, ID) matches.
type candidateSet struct {
	first candidateKey
	keys  map[candidateKey]bool
}

func (c *candidateSet) add(kind, id string) {
	k := candidateKey{kind, id}
	if c.keys == nil {
		c.keys = map[candidateKey]bool{}
		c.first = k
	}
	c.keys[k] = true
}

func (c *candidateSet) n() int { return len(c.keys) }

func matchPeopleByName(dir *PeopleDirectory, name string) candidateSet {
	var set candidateSet
	if name == "" {
		return set
	}
	for _, p := range dir.People {
		if n := NormalizePersonName(p.Name); n != "" && n == name {
			set.add(p.Kind, p.ID)
		}
	}
	return set
}

// ResolveMention applies the spec E3 rules in order. The first rule with one
// candidate wins; several candidates at any rule stop resolution.
func ResolveMention(dir *PeopleDirectory, m PeopleMention) MentionResolution {
	res := MentionResolution{Normalized: NormalizePersonName(m.Text)}
	if dir == nil {
		return res
	}
	won := func(set candidateSet, rule string) MentionResolution {
		res.Kind, res.ID, res.Rule, res.Resolved = set.first.kind, set.first.id, rule, true
		return res
	}

	// Rule 1: exactly one email address in the text.
	emails := peopleEmailPattern.FindAllString(m.Text, -1)
	if len(emails) == 1 {
		var set candidateSet
		for _, p := range dir.People {
			if p.Email != "" && strings.EqualFold(p.Email, emails[0]) {
				set.add(p.Kind, p.ID)
			}
		}
		switch {
		case set.n() == 1:
			return won(set, model.PeopleRuleEmail)
		case set.n() > 1:
			return res
		}
	}

	// Rule 2: the name without the email and brackets, then once stripped.
	name := peopleEmailPattern.ReplaceAllString(m.Text, " ")
	name = NormalizePersonName(strings.NewReplacer("<", " ", ">", " ", "(", " ", ")", " ").Replace(name))
	set := matchPeopleByName(dir, name)
	if set.n() == 0 {
		if stripped, ok := StripHonorific(name); ok {
			set = matchPeopleByName(dir, stripped)
		}
	}
	switch {
	case set.n() == 1:
		return won(set, model.PeopleRuleName)
	case set.n() > 1:
		return res
	}

	// Rule 3: insight entities only, account name/alias or project name.
	if !m.AllowEntities || res.Normalized == "" {
		return res
	}
	var ents candidateSet
	for _, e := range dir.Entities {
		names := append([]string{e.Name}, e.Aliases...)
		for _, n := range names {
			if n := NormalizePersonName(n); n != "" && n == res.Normalized {
				ents.add(e.Kind, e.ID)
				break
			}
		}
	}
	if ents.n() == 1 {
		switch ents.first.kind {
		case model.LinkKindAccount:
			return won(ents, model.PeopleRuleAlias)
		case model.LinkKindProject:
			return won(ents, model.PeopleRuleProject)
		}
	}
	return res
}

func hasControlOrFormat(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return true
		}
	}
	return false
}

// CollectMeetingMentions gathers mentions from participants, the speaker map,
// action item assignees and insight entities, in that order, keeping the first
// MaxPeopleMentionsPerMeeting.
func CollectMeetingMentions(meeting *model.Meeting) (mentions []PeopleMention, truncated bool) {
	if meeting == nil {
		return nil, false
	}
	add := func(m PeopleMention) {
		text := strings.TrimSpace(m.Text)
		if text == "" || hasControlOrFormat(text) || len(m.SourceRef) > model.MaxPeopleSourceRefBytes {
			return
		}
		if len(mentions) >= model.MaxPeopleMentionsPerMeeting {
			truncated = true
			return
		}
		m.Text = capRunes(text, model.MaxPeopleMentionRunes)
		mentions = append(mentions, m)
	}
	speakerName := func(label string) string {
		name := strings.TrimSpace(meeting.SpeakerMap[label])
		if name == "" || defaultSpeakerLabel.MatchString(name) {
			return ""
		}
		return name
	}

	for _, p := range meeting.Participants {
		add(PeopleMention{Text: p, Role: model.PeopleRoleParticipant, SourceField: model.PeopleSourceParticipants})
	}

	labels := make([]string, 0, len(meeting.SpeakerMap))
	for label := range meeting.SpeakerMap {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	for _, label := range labels {
		if name := speakerName(label); name != "" {
			add(PeopleMention{Text: name, Role: model.PeopleRoleSpeaker, SourceField: model.PeopleSourceSpeakerMap, SourceRef: label})
		}
	}

	if meeting.ActionItems != "" {
		var items []ActionItem
		if json.Unmarshal([]byte(meeting.ActionItems), &items) == nil {
			for _, it := range items {
				assignee := strings.TrimSpace(it.Assignee)
				if defaultSpeakerLabel.MatchString(assignee) {
					// A diarization label is only a person through the user's speaker map.
					assignee = speakerName(assignee)
				}
				add(PeopleMention{Text: assignee, Role: model.PeopleRoleAssignee, SourceField: model.PeopleSourceActionItems, SourceRef: it.ID})
			}
		}
	}

	if meeting.Insights != "" {
		var insights []model.MeetingInsight
		if json.Unmarshal([]byte(meeting.Insights), &insights) == nil {
			for _, in := range insights {
				evidence := capRunes(in.Type+": "+in.Text, model.MaxPeopleEvidenceRunes)
				for _, e := range in.Entities {
					add(PeopleMention{Text: e, Role: model.PeopleRoleMentions, SourceField: model.PeopleSourceInsights,
						SourceRef: in.ID, Evidence: evidence, AllowEntities: true})
				}
			}
		}
	}
	return mentions, truncated
}

// PlanMeetingPeople resolves mentions into the meeting's auto edge set and its
// unresolved mentions. The first mention wins each edge's provenance.
func PlanMeetingPeople(accountID, meetingLinkID string, dir *PeopleDirectory, mentions []PeopleMention, now time.Time) (edges []model.PeopleLink, unresolved []model.UnresolvedMention, truncated bool) {
	createdAt := now.UTC().Format(time.RFC3339)
	seenEdges := map[model.PeopleLinkKey]bool{}
	type mentionKey struct{ hash, role string }
	seenMentions := map[mentionKey]bool{}
	for _, m := range mentions {
		res := ResolveMention(dir, m)
		if !res.Resolved {
			if res.Normalized == "" {
				continue
			}
			k := mentionKey{model.MentionHash(res.Normalized), m.Role}
			if seenMentions[k] {
				continue
			}
			seenMentions[k] = true
			unresolved = append(unresolved, model.UnresolvedMention{
				AccountID: accountID, Hash: k.hash, MeetingID: meetingLinkID, Role: m.Role,
				SourceField: m.SourceField, Mention: m.Text, SourceRef: m.SourceRef, CreatedAt: createdAt,
			})
			continue
		}
		key := model.PeopleLinkKey{MeetingID: meetingLinkID, ToKind: res.Kind, ToID: res.ID, Role: m.Role}
		if seenEdges[key] || model.ValidatePeopleLinkKey(key) != nil {
			continue
		}
		if len(edges) >= model.MaxPeopleEdgesPerMeeting {
			truncated = true
			continue
		}
		seenEdges[key] = true
		edges = append(edges, model.PeopleLink{
			AccountID: accountID, MeetingID: meetingLinkID, ToKind: res.Kind, ToID: res.ID, Role: m.Role,
			Origin: model.LinkOriginAuto, Rule: res.Rule, SourceField: m.SourceField, Mention: m.Text,
			SourceRef: m.SourceRef, Evidence: m.Evidence, CreatedAt: createdAt,
		})
	}
	return edges, unresolved, truncated
}
