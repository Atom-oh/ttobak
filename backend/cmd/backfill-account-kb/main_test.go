package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// testCutoff is after every fake object's modification time.
var testCutoff = time.Unix(1800000000, 0)

type fakeObjects struct {
	pages [][]string
	tags  map[string]string
	heads int
}

func (f *fakeObjects) ListKBKeys(_ context.Context, cursor string) ([]string, string, error) {
	i := 0
	if cursor != "" {
		i = int(cursor[0] - '0')
	}
	next := ""
	if i+1 < len(f.pages) {
		next = string(rune('0' + i + 1))
	}
	return f.pages[i], next, nil
}

func (f *fakeObjects) HeadDetail(_ context.Context, key string) (string, time.Time, bool, error) {
	f.heads++
	tag, ok := f.tags[key]
	return tag, time.Unix(1700000000, 0), ok, nil
}

type fakeRepo struct {
	refs     map[string]*model.AccountKBRef
	untagged map[string]bool
	puts     int
}

func (f *fakeRepo) GetAccount(_ context.Context, id string) (*model.Account, error) {
	if id == "acc" {
		return &model.Account{AccountID: id}, nil
	}
	return nil, nil
}

func (f *fakeRepo) PutAccountKBRef(_ context.Context, ref *model.AccountKBRef) error {
	f.puts++
	key := ref.AccountID + "/" + ref.RefID
	if f.untagged[key] {
		return fmt.Errorf("%w: %w", repository.ErrConditionFailed, repository.ErrAccountKBRefUntagged)
	}
	if f.refs[key] != nil {
		return repository.ErrConditionFailed
	}
	f.refs[key] = ref
	return nil
}

func fixture() (*fakeObjects, *fakeRepo) {
	objects := &fakeObjects{
		pages: [][]string{
			{"kb/u1/1700000000000_a.pdf", "kb/u1/b.pdf", "kb/u2/1700000000001_c.pdf"},
			{"kb/u2/deep/d.pdf", "kb/u3/1700000000002_e.pdf", "kb/u3/f.pdf"},
		},
		tags: map[string]string{
			"kb/u1/1700000000000_a.pdf": "acc",
			"kb/u1/b.pdf":               "",
			"kb/u2/1700000000001_c.pdf": "acc",
			"kb/u3/1700000000002_e.pdf": "missing-account",
			"kb/u3/f.pdf":               "a#b",
		},
	}
	return objects, &fakeRepo{refs: map[string]*model.AccountKBRef{}, untagged: map[string]bool{}}
}

func TestBackfill_DryRunWritesNothing(t *testing.T) {
	objects, repo := fixture()
	result, err := backfill(context.Background(), objects, repo, true, 0, testCutoff)
	if err != nil {
		t.Fatal(err)
	}
	if repo.puts != 0 || len(repo.refs) != 0 {
		t.Fatalf("dry run wrote: %d", repo.puts)
	}
	want := backfillResult{Scanned: 6, Tagged: 2, Skipped: 3}
	if result != want {
		t.Fatalf("result %+v want %+v", result, want)
	}
}

func TestBackfill_Idempotent(t *testing.T) {
	objects, repo := fixture()
	first, err := backfill(context.Background(), objects, repo, false, 0, testCutoff)
	if err != nil || first.Written != 2 || first.Existing != 0 {
		t.Fatalf("first: %+v %v", first, err)
	}
	ref := repo.refs["acc/"+model.AccountKBRefID("kb/u1/1700000000000_a.pdf")]
	if ref == nil || ref.UploadedBy != "u1" || ref.FileName != "a.pdf" || ref.S3Key != "kb/u1/1700000000000_a.pdf" || ref.CreatedAt.IsZero() {
		t.Fatalf("ref: %+v", ref)
	}
	second, err := backfill(context.Background(), objects, repo, false, 0, testCutoff)
	if err != nil || second.Written != 0 || second.Existing != 2 || len(repo.refs) != 2 {
		t.Fatalf("second: %+v %v", second, err)
	}
}

func TestBackfill_Limit(t *testing.T) {
	objects, repo := fixture()
	result, err := backfill(context.Background(), objects, repo, false, 2, testCutoff)
	if err != nil || result.Scanned != 2 || result.Written != 1 {
		t.Fatalf("limit: %+v %v", result, err)
	}
}

// A rerun after a member untagged a file must not re-share it.
func TestBackfill_RerunSkipsUntaggedRefs(t *testing.T) {
	objects, repo := fixture()
	if _, err := backfill(context.Background(), objects, repo, false, 0, testCutoff); err != nil {
		t.Fatal(err)
	}
	key := "acc/" + model.AccountKBRefID("kb/u1/1700000000000_a.pdf")
	delete(repo.refs, key)
	repo.untagged[key] = true
	result, err := backfill(context.Background(), objects, repo, false, 0, testCutoff)
	if err != nil || result.Written != 0 || result.Untagged != 1 || result.Existing != 1 || repo.refs[key] != nil {
		t.Fatalf("rerun recreated an untagged ref: %+v %v", result, err)
	}
}

func TestBackfill_ApplyRequiresCutoff(t *testing.T) {
	objects, repo := fixture()
	if _, err := backfill(context.Background(), objects, repo, false, 0, time.Time{}); !errors.Is(err, errCutoffRequired) {
		t.Fatalf("apply without -before: %v", err)
	}
	if repo.puts != 0 || objects.heads != 0 {
		t.Fatalf("refused run touched storage: puts=%d heads=%d", repo.puts, objects.heads)
	}
	// A dry run without a cutoff still counts candidates.
	result, err := backfill(context.Background(), objects, repo, true, 0, time.Time{})
	if err != nil || result.Tagged != 2 || repo.puts != 0 {
		t.Fatalf("dry run without cutoff: %+v %v", result, err)
	}
}

// Objects modified at or after the ref rollout may be ref-era uploads whose
// refs were untagged before tombstones existed; they are never backfilled.
func TestBackfill_SkipsObjectsAtOrAfterCutoff(t *testing.T) {
	modified := time.Unix(1700000000, 0)
	for _, dryRun := range []bool{true, false} {
		for _, cutoff := range []time.Time{modified, modified.Add(-time.Second)} {
			objects, repo := fixture()
			result, err := backfill(context.Background(), objects, repo, dryRun, 0, cutoff)
			if err != nil || result.AfterCutoff != 2 || result.Tagged != 0 || result.Written != 0 || repo.puts != 0 {
				t.Fatalf("dryRun=%v cutoff=%v: %+v %v", dryRun, cutoff, result, err)
			}
		}
	}
}
