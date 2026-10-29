package service

import (
	"context"
	"errors"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

type staleAccountResearchRepo struct {
	*mockResearchRepo
	snapshot model.Research
	readErr  error
}

func (r *staleAccountResearchRepo) BatchGetResearch(context.Context, []string) ([]model.Research, error) {
	return []model.Research{r.snapshot}, nil
}

func (r *staleAccountResearchRepo) GetResearch(ctx context.Context, id string) (*model.Research, error) {
	if r.readErr != nil {
		return nil, r.readErr
	}
	return r.mockResearchRepo.GetResearch(ctx, id)
}

func TestInheritedAccountResearchRejectsStaleSnapshot(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	for _, scenario := range []string{"unlinked", "trashed", "deleted", "storage failure", "identity mismatch", "current"} {
		t.Run(scenario, func(t *testing.T) {
			main := &descendantResearchMainRepo{newMockResearchMainRepo(), newDescendantAccessRepo()}
			snapshot := model.Research{ResearchID: "research", UserID: "owner", AccountIDs: []string{"child"}, Topic: "old", Summary: "old"}
			repo := &staleAccountResearchRepo{mockResearchRepo: newMockResearchRepo(), snapshot: snapshot}
			current := snapshot
			current.Topic, current.Summary = "current", "current"
			repo.byID["research"] = &current
			main.refs["child"] = []model.ResearchRef{{ResearchID: "research"}}
			switch scenario {
			case "unlinked":
				current.AccountIDs = nil
			case "trashed":
				current.TrashedAt = "2026-10-29T00:00:00Z"
			case "deleted":
				delete(repo.byID, "research")
			case "storage failure":
				repo.readErr = errors.New("storage unavailable")
			case "identity mismatch":
				current.ResearchID = "different"
			}
			items, err := newResearchServiceWithRepo(repo, main).ListAccountResearch(context.Background(), "reader", "child")
			if scenario == "storage failure" {
				if !errors.Is(err, repo.readErr) || items != nil {
					t.Fatalf("storage failure hidden: %+v/%v", items, err)
				}
			} else if scenario == "current" {
				if err != nil || len(items) != 1 || items[0].Topic != "current" || items[0].Summary != "current" {
					t.Fatalf("current research not rendered: %+v/%v", items, err)
				}
			} else if err != nil || len(items) != 0 {
				t.Fatalf("stale research exposed: %+v/%v", items, err)
			}
		})
	}
}
