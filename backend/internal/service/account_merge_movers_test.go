package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

type fakeMoverRepo struct {
	contactFake
	eventFake
	meetingFake
	members map[string]map[string]*model.AccountMember
	puts    []string
	putErr  error
	onPut   func()
	// memberLists counts ListAccountMembers calls.
	memberLists int
}

func newFakeMoverRepo() *fakeMoverRepo {
	return &fakeMoverRepo{members: map[string]map[string]*model.AccountMember{}}
}

func (f *fakeMoverRepo) add(account, user, role string) {
	if f.members[account] == nil {
		f.members[account] = map[string]*model.AccountMember{}
	}
	f.members[account][user] = &model.AccountMember{AccountID: account, UserID: user, Role: role}
}

func (f *fakeMoverRepo) ListAccountMembers(_ context.Context, account string) ([]model.AccountMember, error) {
	f.memberLists++
	var out []model.AccountMember
	for _, m := range f.members[account] {
		out = append(out, *m)
	}
	return out, nil
}

func (f *fakeMoverRepo) GetMember(_ context.Context, account, user string) (*model.AccountMember, error) {
	return f.members[account][user], nil
}

func (f *fakeMoverRepo) PutMergedMember(_ context.Context, _ *model.AccountMergeJob, m *model.AccountMember) error {
	if f.onPut != nil {
		f.onPut()
	}
	if f.putErr != nil {
		return f.putErr
	}
	if f.members[m.AccountID][m.UserID] != nil {
		return nil // create-only: an existing target member keeps its role
	}
	f.add(m.AccountID, m.UserID, m.Role)
	f.puts = append(f.puts, m.UserID)
	return nil
}

func moverFixture(repo *fakeMoverRepo) (*AccountMergeService, *model.AccountMergeJob) {
	return &AccountMergeService{mover: repo}, &model.AccountMergeJob{SourceAccountID: "src", TargetAccountID: "tgt",
		RequesterUserID: "owner", LeaseOwner: "w1", State: model.MergeStateRunning, Step: "members"}
}

func TestMoveMembersRoleMapping(t *testing.T) {
	repo := newFakeMoverRepo()
	repo.add("src", "owner", model.RoleOwner)
	repo.add("src", "am", model.RoleAM)
	repo.add("src", "sa2", model.RoleSA)
	repo.add("src", "both", model.RoleTAM)
	repo.add("tgt", "both", model.RoleAMManager)
	s, job := moverFixture(repo)
	res, err := s.moveMembers(newMergeFence(context.Background()), job)
	if err != nil {
		t.Fatal(err)
	}
	if res.Moved != 3 || !res.Done {
		t.Fatalf("moved = %d done = %v, want 3 true", res.Moved, res.Done)
	}
	for user, want := range map[string]string{"owner": model.RoleSA, "am": model.RoleAM, "sa2": model.RoleSA, "both": model.RoleAMManager} {
		if got := repo.members["tgt"][user]; got == nil || got.Role != want {
			t.Errorf("target member %s = %+v, want role %s", user, got, want)
		}
	}
	if len(repo.members["src"]) != 4 {
		t.Errorf("source members must stay in place, got %d", len(repo.members["src"]))
	}
}

func TestMoveMembers_Idempotent(t *testing.T) {
	repo := newFakeMoverRepo()
	repo.add("src", "owner", model.RoleOwner)
	repo.add("src", "am", model.RoleAM)
	s, job := moverFixture(repo)
	fence := newMergeFence(context.Background())
	if _, err := s.moveMembers(fence, job); err != nil {
		t.Fatal(err)
	}
	res, err := s.moveMembers(fence, job)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if res.Moved != 0 || len(repo.puts) != 2 {
		t.Fatalf("second run moved %d, total puts %d; want 0 and 2", res.Moved, len(repo.puts))
	}
}

func TestMergeFenceLostStopsMover(t *testing.T) {
	t.Run("cancelled before start", func(t *testing.T) {
		repo := newFakeMoverRepo()
		repo.add("src", "am", model.RoleAM)
		s, job := moverFixture(repo)
		job.Cursor = "a"
		fence := newMergeFence(context.Background())
		fence.Lose()
		res, err := s.moveMembers(fence, job)
		if !errors.Is(err, ErrMergeFenceLost) || res.Cursor != "a" || len(repo.puts) != 0 {
			t.Fatalf("err=%v cursor=%q puts=%v", err, res.Cursor, repo.puts)
		}
	})
	t.Run("lost mid run", func(t *testing.T) {
		repo := newFakeMoverRepo()
		repo.add("src", "a", model.RoleAM)
		repo.add("src", "b", model.RoleSA)
		s, job := moverFixture(repo)
		fence := newMergeFence(context.Background())
		repo.onPut = fence.Lose
		res, err := s.moveMembers(fence, job)
		if !errors.Is(err, ErrMergeFenceLost) || res.Cursor != "" || len(repo.puts) != 1 {
			t.Fatalf("err=%v cursor=%q puts=%v", err, res.Cursor, repo.puts)
		}
	})
	t.Run("repository reports lost fence", func(t *testing.T) {
		repo := newFakeMoverRepo()
		repo.add("src", "a", model.RoleAM)
		repo.putErr = ErrMergeFenceLost
		s, job := moverFixture(repo)
		fence := newMergeFence(context.Background())
		res, err := s.moveMembers(fence, job)
		if !errors.Is(err, ErrMergeFenceLost) || res.Cursor != "" || fence.Err() == nil {
			t.Fatalf("err=%v cursor=%q fence=%v", err, res.Cursor, fence.Err())
		}
	})
}

func TestMoveMembersBoundedPerCall(t *testing.T) {
	mover := newFakeMoverRepo()
	for i := 0; i < mergeMembersPerRun+5; i++ {
		mover.add("a", fmt.Sprintf("u%03d", i), model.RoleSA)
	}
	s := &AccountMergeService{mover: mover}
	job := &model.AccountMergeJob{SourceAccountID: "a", TargetAccountID: "b", RequesterUserID: "owner", LeaseOwner: "w1"}
	first, err := s.moveMembers(newMergeFence(context.Background()), job)
	if err != nil || first.Done || first.Moved != mergeMembersPerRun || first.Cursor != fmt.Sprintf("u%03d", mergeMembersPerRun-1) {
		t.Fatalf("first = %+v err=%v", first, err)
	}
	job.Cursor = first.Cursor
	second, err := s.moveMembers(newMergeFence(context.Background()), job)
	if err != nil || !second.Done || second.Moved != 5 {
		t.Fatalf("second = %+v err=%v", second, err)
	}
}
