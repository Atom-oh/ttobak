package service

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

const updateContractsDir = "../../../contracts/customer-updates/"

type updateDraftCheckContract struct {
	BareHostTlds []string `json:"bareHostTlds"`
	Cases        []struct {
		Name       string `json:"name"`
		Body       string `json:"body"`
		BodyRepeat *struct {
			Text  string `json:"text"`
			Count int    `json:"count"`
		} `json:"bodyRepeat"`
		Links       []string `json:"links"`
		AllowedURLs []string `json:"allowedUrls"`
		BlockList   []string `json:"blockList"`
		Stages      []string `json:"stages"`
		Expect      []string `json:"expect"`
	} `json:"cases"`
	EscapeCases []struct {
		In  string `json:"in"`
		Out string `json:"out"`
	} `json:"escapeCases"`
}

func loadUpdateContract(t *testing.T, name string, v any) {
	t.Helper()
	data, err := os.ReadFile(updateContractsDir + name)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatal(err)
	}
}

func TestCheckUpdateDraftContract(t *testing.T) {
	var c updateDraftCheckContract
	loadUpdateContract(t, "draft-checks-v1.json", &c)
	if len(c.Cases) == 0 {
		t.Fatal("no cases")
	}
	seen := map[string]bool{}
	for _, tc := range c.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			body := tc.Body
			if tc.BodyRepeat != nil {
				body = strings.Repeat(tc.BodyRepeat.Text, tc.BodyRepeat.Count)
			}
			in := UpdateDraftCheckInput{
				Body: body, Links: tc.Links, AllowedURLs: tc.AllowedURLs,
				BlockList: tc.BlockList, Stages: tc.Stages,
			}
			if err := ValidateUpdateDraftEncoding(in); err != nil {
				t.Fatalf("contract input rejected: %v", err)
			}
			got := CheckUpdateDraft(in)
			want := tc.Expect
			if want == nil {
				want = []string{}
			}
			if got == nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("codes = %#v, want %#v", got, want)
			}
			for _, code := range got {
				seen[code] = true
			}
		})
	}
	for _, code := range updateDraftCheckCodes {
		if !seen[code] {
			t.Errorf("no contract case reports %q", code)
		}
	}
}

func TestEscapeUpdateSlackContract(t *testing.T) {
	var c updateDraftCheckContract
	loadUpdateContract(t, "draft-checks-v1.json", &c)
	if len(c.EscapeCases) == 0 {
		t.Fatal("no escape cases")
	}
	for _, tc := range c.EscapeCases {
		if got := EscapeUpdateSlack(tc.In); got != tc.Out {
			t.Errorf("EscapeUpdateSlack(%q) = %q, want %q", tc.In, got, tc.Out)
		}
	}
}

func TestCheckUpdateDraftTermsMirrorContract(t *testing.T) {
	var terms struct {
		OpportunityWords  []string `json:"opportunityWords"`
		MeetingOfferWords []string `json:"meetingOfferWords"`
		FigureUnits       []string `json:"figureUnits"`
		FigureExamples    []string `json:"figureExamples"`
	}
	loadUpdateContract(t, "draft-terms-ko-v1.json", &terms)
	if !reflect.DeepEqual(terms.OpportunityWords, updateOpportunityWords) {
		t.Errorf("opportunity words differ: %v vs %v", updateOpportunityWords, terms.OpportunityWords)
	}
	if !reflect.DeepEqual(terms.MeetingOfferWords, updateMeetingOfferWords) {
		t.Errorf("meeting offer words differ: %v vs %v", updateMeetingOfferWords, terms.MeetingOfferWords)
	}
	if !reflect.DeepEqual(terms.FigureUnits, updateFigureUnits) {
		t.Errorf("figure units differ: %v vs %v", updateFigureUnits, terms.FigureUnits)
	}
	for _, ex := range terms.FigureExamples {
		if !hasUpdateFigure([]rune(ex)) {
			t.Errorf("figure example %q not detected", ex)
		}
	}
}

func TestCheckUpdateDraftBareHostTldsMirrorContract(t *testing.T) {
	var c updateDraftCheckContract
	loadUpdateContract(t, "draft-checks-v1.json", &c)
	if len(c.BareHostTlds) == 0 || !reflect.DeepEqual(c.BareHostTlds, updateBareHostTLDs) {
		t.Fatalf("bare host TLDs differ:\n go: %v\n contract: %v", updateBareHostTLDs, c.BareHostTlds)
	}
}

func TestCheckUpdateDraftInternalHostFromModel(t *testing.T) {
	// The internal host list itself is pinned by model.TestInternalHostsMirrorContract.
	in := UpdateDraftCheckInput{
		Body:        "자료 https://team.slack.com/x 미팅",
		Links:       []string{"https://team.slack.com/x"},
		AllowedURLs: []string{"https://team.slack.com/x"},
	}
	if got := CheckUpdateDraft(in); !reflect.DeepEqual(got, []string{"link_internal"}) {
		t.Fatalf("got %v", got)
	}
}

func TestValidateUpdateDraftEncoding(t *testing.T) {
	bad := "a\xffb"
	ok := UpdateDraftCheckInput{
		Body: "본문", Links: []string{"https://example.com/a"}, AllowedURLs: []string{"https://example.com/a"},
		BlockList: []string{"이름"}, Stages: []string{"Stage"},
	}
	cases := []struct {
		name   string
		mutate func(in *UpdateDraftCheckInput)
		bad    bool
	}{
		{"valid", func(in *UpdateDraftCheckInput) {}, false},
		{"empty", func(in *UpdateDraftCheckInput) { *in = UpdateDraftCheckInput{} }, false},
		{"body", func(in *UpdateDraftCheckInput) { in.Body = bad }, true},
		{"truncated_rune_body", func(in *UpdateDraftCheckInput) { in.Body = "가"[:2] }, true},
		{"link", func(in *UpdateDraftCheckInput) { in.Links = []string{"https://example.com/a", bad} }, true},
		{"allowed", func(in *UpdateDraftCheckInput) { in.AllowedURLs = []string{bad} }, true},
		{"block_list", func(in *UpdateDraftCheckInput) { in.BlockList = []string{bad} }, true},
		{"stages", func(in *UpdateDraftCheckInput) { in.Stages = []string{bad} }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := ok
			in.Links = append([]string(nil), ok.Links...)
			tc.mutate(&in)
			err := ValidateUpdateDraftEncoding(in)
			if tc.bad != (err != nil) {
				t.Fatalf("err = %v, want bad=%v", err, tc.bad)
			}
			if tc.bad && !errors.Is(err, ErrUpdateDraftEncoding) {
				t.Fatalf("err = %v, want ErrUpdateDraftEncoding", err)
			}
		})
	}
}
