package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/repository"
)

type fakeUpserter struct {
	outcomes map[string]repository.OperatorSourceOutcome
	errs     map[string]error
	calls    []string
	applied  []bool
}

func (f *fakeUpserter) UpsertOperatorNewsSource(_ context.Context, src repository.OperatorNewsSource, apply bool) (repository.OperatorSourceOutcome, error) {
	f.calls = append(f.calls, src.SourceID)
	f.applied = append(f.applied, apply)
	if err := f.errs[src.SourceID]; err != nil {
		return "", err
	}
	return f.outcomes[src.SourceID], nil
}

func TestSeedDefinitionsPassCrawlerValidation(t *testing.T) {
	if err := validateSources(regulatorySources); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"fss-kr": true, "fsc-kr": true, "fsec-kr": true, "kr-finance-law": true}
	for _, src := range regulatorySources {
		delete(want, src.SourceID)
		if len(src.Aliases) == 0 || len(src.SearchDomains) == 0 {
			t.Errorf("%s needs an English alias and a domain allowlist", src.SourceID)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing regulatory sources: %v", want)
	}
}

func TestSeedValidationRejectsWhatTheCrawlerRejects(t *testing.T) {
	base := regulatorySources[0]
	for name, mutate := range map[string]func(*repository.OperatorNewsSource){
		"reserved id":    func(s *repository.OperatorNewsSource) { s.SourceID = "__auto__" },
		"spaced id":      func(s *repository.OperatorNewsSource) { s.SourceID = "fss kr" },
		"empty name":     func(s *repository.OperatorNewsSource) { s.SourceName = "" },
		"quoted alias":   func(s *repository.OperatorNewsSource) { s.Aliases = []string{`"FSS"`} },
		"long query":     func(s *repository.OperatorNewsSource) { s.NewsQueries = []string{strings.Repeat("가", 201)} },
		"dup query":      func(s *repository.OperatorNewsSource) { s.NewsQueries = []string{"a", "a"} },
		"url domain":     func(s *repository.OperatorNewsSource) { s.SearchDomains = []string{"https://fss.or.kr"} },
		"upper domain":   func(s *repository.OperatorNewsSource) { s.SearchDomains = []string{"FSS.or.kr"} },
		"ip domain":      func(s *repository.OperatorNewsSource) { s.SearchDomains = []string{"10.0.0.1"} },
		"single label":   func(s *repository.OperatorNewsSource) { s.SearchDomains = []string{"localhost"} },
		"too many hosts": func(s *repository.OperatorNewsSource) { s.SearchDomains = manyDomains(101) },
	} {
		src := base
		mutate(&src)
		if err := validateSources([]repository.OperatorNewsSource{src}); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := validateSources([]repository.OperatorNewsSource{base, base}); err == nil {
		t.Error("duplicate source IDs accepted")
	}
}

func manyDomains(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("d%d.com", i)
	}
	return out
}

func TestRunDefaultsToReadOnlyAndReportsEachSource(t *testing.T) {
	fake := &fakeUpserter{outcomes: map[string]repository.OperatorSourceOutcome{
		"fss-kr": repository.OperatorSourceCreated, "fsc-kr": repository.OperatorSourceUnchanged,
		"fsec-kr": repository.OperatorSourceUpdated, "kr-finance-law": repository.OperatorSourceCreated,
	}}
	var out bytes.Buffer
	if err := run(context.Background(), fake, regulatorySources, "", false, &out); err != nil {
		t.Fatal(err)
	}
	for _, applied := range fake.applied {
		if applied {
			t.Fatal("dry run requested a write")
		}
	}
	for _, line := range []string{"dry-run fss-kr CREATE", "dry-run fsc-kr UNCHANGED", "dry-run fsec-kr UPDATE"} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("missing %q in %q", line, out.String())
		}
	}
}

func TestRunContinuesPastRefusalAndFailsOverall(t *testing.T) {
	fake := &fakeUpserter{errs: map[string]error{
		"fss-kr": fmt.Errorf("%w: fss-kr", repository.ErrNotOperatorSource),
		"fsc-kr": errors.New("throttled"),
	}, outcomes: map[string]repository.OperatorSourceOutcome{}}
	var out bytes.Buffer
	err := run(context.Background(), fake, regulatorySources, "", true, &out)
	if err == nil || len(fake.calls) != len(regulatorySources) {
		t.Fatalf("err=%v calls=%v", err, fake.calls)
	}
	if !strings.Contains(out.String(), "apply fss-kr REFUSED") || !strings.Contains(out.String(), "apply fsc-kr FAILED") {
		t.Fatalf("output: %q", out.String())
	}
}

func TestRunSingleSourceAndUnknownSource(t *testing.T) {
	fake := &fakeUpserter{outcomes: map[string]repository.OperatorSourceOutcome{"fsec-kr": repository.OperatorSourceCreated}}
	var out bytes.Buffer
	if err := run(context.Background(), fake, regulatorySources, "fsec-kr", true, &out); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 1 || fake.calls[0] != "fsec-kr" || !fake.applied[0] {
		t.Fatalf("calls=%v applied=%v", fake.calls, fake.applied)
	}
	if err := run(context.Background(), fake, regulatorySources, "nope", true, &out); err == nil {
		t.Fatal("unknown source accepted")
	}
}
