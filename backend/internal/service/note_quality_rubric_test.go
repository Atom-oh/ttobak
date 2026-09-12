package service

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

type qualityCriterion struct {
	ID            string   `json:"id"`
	Scope         string   `json:"scope"`
	Required      []string `json:"required"`
	Forbidden     []string `json:"forbidden"`
	Target        string   `json:"target"`
	LineRequires  string   `json:"lineRequires"`
	LineForbids   string   `json:"lineForbids"`
	NoTasks       bool     `json:"noTasks"`
	NoDates       bool     `json:"noDates"`
	UnknownOwner  bool     `json:"unknownOwner"`
	NoCitationFor string   `json:"noCitationFor"`
	NoApproval    bool     `json:"noApproval"`
}

type qualityCase struct {
	ID, Description, TranscriptA, TranscriptB, SelectedTranscript, Notes string
	Segments                                                             []speakerSegment
	Criteria                                                             []qualityCriterion
	Reference                                                            string
	Mutations                                                            []struct {
		Name, From, To string
		Fails          []string
	}
}

type qualityFinding struct {
	ID     string `json:"id"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

func loadQualityCases(t *testing.T) ([]qualityCase, []byte) {
	t.Helper()
	data, err := os.ReadFile("testdata/note-quality/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []qualityCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != 4 {
		t.Fatalf("reference corpus changed unexpectedly: %d", len(cases))
	}
	seen := map[string]bool{}
	for _, fixture := range cases {
		if fixture.ID == "" || seen[fixture.ID] || len(fixture.Criteria) == 0 {
			t.Fatal("fixture IDs must be unique and every fixture needs criteria")
		}
		seen[fixture.ID] = true
		criteria := map[string]bool{"nonempty": true, "anchor-integrity": true}
		for _, criterion := range fixture.Criteria {
			if criterion.ID == "" || criteria[criterion.ID] {
				t.Fatalf("%s has a missing/duplicate criterion ID", fixture.ID)
			}
			criteria[criterion.ID] = true
		}
	}
	return cases, data
}

func qualityMeeting(fixture qualityCase) *model.Meeting {
	texts := make([]string, len(fixture.Segments))
	for i, segment := range fixture.Segments {
		texts[i] = segment.Text
	}
	a, b := fixture.TranscriptA, fixture.TranscriptB
	if fixture.SelectedTranscript == "B" && b == "" {
		b = strings.Join(texts, " ")
	} else if a == "" {
		a = strings.Join(texts, " ")
	}
	segments, err := json.Marshal(fixture.Segments)
	if err != nil {
		panic(err) // Invalid checked-in fixture, never application input.
	}
	return &model.Meeting{UserID: "eval-owner", MeetingID: fixture.ID,
		Title: fixture.Description, TranscriptA: a, TranscriptB: b,
		SelectedTranscript: fixture.SelectedTranscript, TranscriptSegments: string(segments), Notes: fixture.Notes}
}

func qualityNormalized(text string) string {
	return strings.ToLower(strings.ReplaceAll(strings.Join(strings.Fields(text), ""), ",", ""))
}

func qualitySections(note string) map[string]string {
	sections := map[string]string{"all": note}
	current := ""
	for _, line := range strings.Split(note, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "## ") {
			heading := qualityNormalized(strings.TrimSpace(line)[3:])
			current = ""
			switch {
			case strings.Contains(heading, "결정"), strings.Contains(heading, "decisions"):
				current = "decisions"
			case strings.Contains(heading, "액션"), strings.Contains(heading, "action"):
				current = "actions"
			case strings.Contains(heading, "참석자"), strings.Contains(heading, "participants"):
				current = "participants"
			}
		} else if current != "" {
			sections[current] += line + "\n"
		}
	}
	return sections
}

var qualityLink = regexp.MustCompile(`\[[^\]]*\]\(transcript://[^)]+\)`)
var qualityNoTasks = regexp.MustCompile(`^(?:(?:원문|녹취록|이번회의|본회의)(?:에서|에는|에|상))?(?:명시적으로)?(?:확정된|합의된|명시된)?(?:액션아이템|후속작업)(?:은|이|는|가)?(?:없습니다|없음|없다|확인되지않았습니다|확정되지않았습니다)$`)
var qualityDate = regexp.MustCompile(`(?:20\d\d[-./년])?\d{1,2}[-./월]\d{1,2}(?:일)?|내일|다음주|이번주|월말|주말`)
var qualityApproval = regexp.MustCompile(`배포.{0,70}승인(?:했|합니다|되었습니다|됨|완료|으로확정|되었|으로(?:변경|전환)(?:되어|되었|됐|했|하였|함))|승인된배포`)

func onlyNoTaskResult(scope string) bool {
	found := false
	for _, line := range strings.Split(scope, "\n") {
		value := qualityNormalized(qualityLink.ReplaceAllString(line, ""))
		value = strings.Trim(value, "-*。.!:;•")
		if value == "" {
			continue
		}
		if !qualityNoTasks.MatchString(value) {
			return false
		}
		found = true
	}
	return found
}

func explicitUnknownOwner(line string) bool {
	value := qualityNormalized(qualityLink.ReplaceAllString(line, ""))
	value = strings.NewReplacer("**", "", "__", "", "`", "", "(", "", ")", "").Replace(value)
	value = strings.TrimLeft(value, "-*[]x0123456789.")
	if before, _, found := strings.Cut(value, ":"); found {
		return before == "미정" || before == "담당미정" || before == "담당자미정" ||
			before == "미지정" || before == "담당미지정" || before == "담당자미지정"
	}
	return regexp.MustCompile(`담당자?(?:와기한|및기한)?(?:은|는|이)?(?:아직)?(?:미정|미지정|미확정)`).MatchString(value)
}

func gradeNoteQuality(fixture qualityCase, note string) []qualityFinding {
	findings := []qualityFinding{{ID: "nonempty", Passed: strings.TrimSpace(note) != ""}}
	meeting := qualityMeeting(fixture)
	selected, _ := selectMeetingTranscript(meeting)
	allowed := map[string]bool{}
	for _, segment := range transcriptSegmentsForText(selected, meeting.TranscriptSegments) {
		if segment.ID != "" {
			allowed[segment.ID] = true
		}
	}
	links := regexp.MustCompile(`transcript://([^\s)]+)`).FindAllStringSubmatch(note, -1)
	valid := len(links) > 0 && !strings.Contains(note, "[TS:")
	for _, link := range links {
		valid = valid && allowed[link[1]]
	}
	findings = append(findings, qualityFinding{ID: "anchor-integrity", Passed: valid,
		Detail: "Require existing verified segment IDs; this does not prove every claim's semantic citation alignment."})
	sections := qualitySections(note)
	for _, criterion := range fixture.Criteria {
		scope := sections[criterion.Scope]
		normalized := qualityNormalized(scope)
		finding := qualityFinding{ID: criterion.ID, Passed: true}
		fail := func(detail string) {
			finding.Passed = false
			if finding.Detail != "" {
				finding.Detail += "; "
			}
			finding.Detail += detail
		}
		switch criterion.Scope {
		case "all", "decisions", "actions", "participants":
		default:
			panic("unknown quality scope: " + criterion.Scope)
		}
		for _, pattern := range criterion.Required {
			if !regexp.MustCompile(pattern).MatchString(normalized) {
				fail("required fact not found: " + pattern)
			}
		}
		for _, pattern := range criterion.Forbidden {
			if regexp.MustCompile(pattern).MatchString(normalized) {
				fail("forbidden claim/evidence found: " + pattern)
			}
		}
		if criterion.NoTasks && !onlyNoTaskResult(scope) {
			fail("require an explicit no-task result, without inferred work in any bullet format")
		}
		if criterion.NoApproval && qualityApproval.MatchString(strings.ReplaceAll(normalized, "미승인", "NEGATED_APPROVAL")) {
			fail("contradictory deployment approval")
		}
		if criterion.NoCitationFor != "" {
			for _, line := range strings.Split(scope, "\n\n") {
				if regexp.MustCompile(criterion.NoCitationFor).MatchString(qualityNormalized(line)) && strings.Contains(line, "transcript://") {
					fail("notes-only fact shares a transcript citation")
				}
			}
		}
		if criterion.Target != "" {
			for _, line := range strings.Split(scope, "\n") {
				value := qualityNormalized(line)
				if !strings.Contains(value, qualityNormalized(criterion.Target)) {
					continue
				}
				if criterion.LineRequires != "" && !regexp.MustCompile(criterion.LineRequires).MatchString(value) {
					fail(fmt.Sprintf("%s lacks its uncertainty label", criterion.Target))
				}
				if criterion.LineForbids != "" && regexp.MustCompile(criterion.LineForbids).MatchString(value) {
					fail(fmt.Sprintf("%s has an unsupported claim", criterion.Target))
				}
				if criterion.NoDates && qualityDate.MatchString(qualityNormalized(qualityLink.ReplaceAllString(line, ""))) {
					fail(fmt.Sprintf("%s has an invented deadline", criterion.Target))
				}
				if criterion.UnknownOwner && !explicitUnknownOwner(line) {
					fail(fmt.Sprintf("%s must retain an explicitly unknown owner", criterion.Target))
				}
			}
		}
		findings = append(findings, finding)
	}
	return findings
}

func TestNoteQualityRubricAcceptsReferencesAndRejectsMutations(t *testing.T) {
	cases, _ := loadQualityCases(t)
	for _, fixture := range cases {
		t.Run(fixture.ID, func(t *testing.T) {
			good := gradeNoteQuality(fixture, fixture.Reference)
			if len(good) != len(fixture.Criteria)+2 {
				t.Fatalf("criteria were not all evaluated: %+v", good)
			}
			for _, finding := range good {
				if !finding.Passed {
					t.Errorf("reviewed reference rejected: %+v", finding)
				}
			}
			for _, mutation := range fixture.Mutations {
				t.Run(mutation.Name, func(t *testing.T) {
					if !strings.Contains(fixture.Reference, mutation.From) {
						t.Fatal("mutation does not alter the reference")
					}
					bad := strings.Replace(fixture.Reference, mutation.From, mutation.To, 1)
					failed := map[string]bool{}
					for _, finding := range gradeNoteQuality(fixture, bad) {
						failed[finding.ID] = !finding.Passed
					}
					for _, id := range mutation.Fails {
						if !failed[id] {
							t.Errorf("deliberate error not detected: %s", id)
						}
					}
				})
			}
		})
	}
}

func TestNoteQualityRubricRejectsMissingAndInventedEvidence(t *testing.T) {
	cases, _ := loadQualityCases(t)
	for _, text := range []string{"", "잘 작성된 회의록입니다.", cases[0].Reference + "\n[근거](transcript://invented)"} {
		failed := map[string]bool{}
		for _, result := range gradeNoteQuality(cases[0], text) {
			failed[result.ID] = !result.Passed
		}
		if !failed["anchor-integrity"] {
			t.Fatalf("missing/forged evidence accepted: %q", text)
		}
	}
}
