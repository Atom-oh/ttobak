package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

func stepJob(step, cursor string) *model.AccountMergeJob {
	return &model.AccountMergeJob{SourceAccountID: "a", TargetAccountID: "b", RequesterUserID: "u1",
		State: model.MergeStateRunning, Step: step, Cursor: cursor, LeaseOwner: "w1"}
}

func TestMergeChildrenStep(t *testing.T) {
	h := newWorkerHarness("children")
	h.repo.addAccount("c1", "a")
	h.repo.addAccount("c2", "a").ParentAccountID = "x" // moved elsewhere; stale CHILDREF on a
	h.repo.addAccount("c3", "a").MergingInto = "z"     // itself merging
	h.repo.addAccount("c4", "a").MergedInto = "z"      // tombstoned
	h.worker.reparentErr = map[string]error{"c2": repository.ErrConditionFailed, "c3": repository.ErrConditionFailed,
		"c4": repository.ErrConditionFailed}
	res, err := h.svc.moveChildren(newMergeFence(context.Background()), stepJob("children", ""))
	if err != nil {
		t.Fatal(err)
	}
	if res.Moved != 1 || res.Skipped != 3 || !res.Done || res.Cursor != "" || !reflect.DeepEqual(h.worker.reparented, []string{"c1"}) {
		t.Fatalf("res=%+v reparented=%v", res, h.worker.reparented)
	}
	want := []repository.AccountParentLink{{AccountID: "b", ParentAccountID: "p"}, {AccountID: "p"}}
	if !reflect.DeepEqual(h.worker.chains[0], want) {
		t.Fatalf("chain = %+v, want %+v", h.worker.chains[0], want)
	}

	// A child still under the source is retried, not skipped.
	blocked := newWorkerHarness("children")
	blocked.repo.addAccount("c1", "a")
	blocked.worker.reparentErr = map[string]error{"c1": repository.ErrConditionFailed}
	res, err = blocked.svc.moveChildren(newMergeFence(context.Background()), stepJob("children", ""))
	if !errors.Is(err, repository.ErrConditionFailed) || res.Cursor != "" || res.Skipped != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}

	// More children than one call moves leave a page cursor.
	paged := newWorkerHarness("children")
	for i := 0; i < mergeLinksPerRun+2; i++ {
		paged.repo.addAccount(fmt.Sprintf("k%02d", i), "a")
	}
	res, err = paged.svc.moveChildren(newMergeFence(context.Background()), stepJob("children", ""))
	if err != nil || res.Done || res.Cursor == "" || res.Moved != mergeLinksPerRun {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestMergeLinkStepsCursor(t *testing.T) {
	h := newWorkerHarness("projects")
	for i := 29; i >= 0; i-- {
		h.worker.projects = append(h.worker.projects, model.ProjectRef{ProjectID: fmt.Sprintf("p%02d", i)})
	}
	h.worker.research = []model.ResearchRef{{ResearchID: "r2"}, {ResearchID: "r1"}}
	fence := newMergeFence(context.Background())
	first, err := h.svc.moveProjects(fence, stepJob("projects", ""))
	if err != nil || first.Done || first.Moved != mergeLinksPerRun || first.Cursor != "p24" {
		t.Fatalf("first = %+v err=%v", first, err)
	}
	second, err := h.svc.moveProjects(fence, stepJob("projects", first.Cursor))
	if err != nil || !second.Done || second.Moved != 5 || second.Cursor != "" {
		t.Fatalf("second = %+v err=%v", second, err)
	}
	if len(h.worker.movedProjects) != 30 || h.worker.movedProjects[0] != "p00" || h.worker.movedProjects[29] != "p29" {
		t.Fatalf("moved = %v", h.worker.movedProjects)
	}
	res, err := h.svc.moveResearch(fence, stepJob("research", ""))
	if err != nil || !res.Done || !reflect.DeepEqual(h.worker.movedResearch, []string{"r1", "r2"}) {
		t.Fatalf("research res=%+v moved=%v err=%v", res, h.worker.movedResearch, err)
	}
}

func TestMergeMetadataStep(t *testing.T) {
	h := newWorkerHarness("metadata")
	b := h.repo.accounts["b"]
	b.Name, b.Aliases, b.Domains = "LG Electronics", []string{"LGE Korea"}, []string{"lg.com"}
	a := h.repo.accounts["a"]
	a.Name, a.Aliases, a.Domains = "lg electronics ", []string{"LGE", " lge korea ", "엘지전자"}, []string{"LG.com", "lge.com"}
	res, err := h.svc.moveMetadata(newMergeFence(context.Background()), stepJob("metadata", ""))
	if err != nil || !res.Done || res.Moved != 3 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	want := [2][]string{{"LGE Korea", "LGE", "엘지전자"}, {"lg.com", "lge.com"}}
	if len(h.worker.metaWrites) != 1 || !reflect.DeepEqual(h.worker.metaWrites[0], want) {
		t.Fatalf("writes = %v, want %v", h.worker.metaWrites, want)
	}

	// The cap keeps every target term and fills the rest from the source.
	capped := newWorkerHarness("metadata")
	var own []string
	for i := 0; i < mergeMaxTerms-1; i++ {
		own = append(own, fmt.Sprintf("b%d", i))
	}
	capped.repo.accounts["b"].Aliases = own
	capped.repo.accounts["a"].Name, capped.repo.accounts["a"].Aliases = "A", []string{"a1", "a2"}
	if _, err := capped.svc.moveMetadata(newMergeFence(context.Background()), stepJob("metadata", "")); err != nil {
		t.Fatal(err)
	}
	aliases := capped.worker.metaWrites[0][0]
	if len(aliases) != mergeMaxTerms || !reflect.DeepEqual(aliases[:len(own)], own) || aliases[len(own)] != "A" {
		t.Fatalf("aliases = %v", aliases)
	}

	// An over-cap target keeps all its terms and nothing is written.
	full := newWorkerHarness("metadata")
	for i := 0; i < mergeMaxTerms+5; i++ {
		full.repo.accounts["b"].Aliases = append(full.repo.accounts["b"].Aliases, fmt.Sprintf("b%d", i))
	}
	full.repo.accounts["a"].Name = "A"
	res, err = full.svc.moveMetadata(newMergeFence(context.Background()), stepJob("metadata", ""))
	if err != nil || !res.Done || res.Skipped != 1 || len(full.worker.metaWrites) != 0 {
		t.Fatalf("res=%+v writes=%v err=%v", res, full.worker.metaWrites, err)
	}

	// A concurrent target edit is returned for a fresh read and retry.
	stale := newWorkerHarness("metadata")
	stale.repo.accounts["a"].Name = "A"
	stale.worker.metaErr = repository.ErrConditionFailed
	if _, err := stale.svc.moveMetadata(newMergeFence(context.Background()), stepJob("metadata", "")); !errors.Is(err, repository.ErrConditionFailed) {
		t.Fatalf("err = %v", err)
	}
}
