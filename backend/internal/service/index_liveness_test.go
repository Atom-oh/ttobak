package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

type indexMemberReadFailure struct {
	IndexRepository
	key    model.IndexResource
	active bool
	err    error
}

func (r *indexMemberReadFailure) GetIndexSource(ctx context.Context, key model.IndexResource) (*model.IndexRecord, error) {
	if r.active && key == r.key {
		return nil, r.err
	}
	return r.IndexRepository.GetIndexSource(ctx, key)
}

func TestIndexPersistentMemberFailuresDoNotHoldOtherGenerations(t *testing.T) {
	for _, phase := range []string{"catalogue", "exporting", "after-put", "orphaned", "finalizing-read", "unknown-status"} {
		t.Run(phase, func(t *testing.T) {
			s, repo, objects, provider, now := newIndexTest()
			bad := addIndexMeeting(repo, "blocked", "last published notes")
			if _, err := s.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			finishIndex(t, s, provider)
			oldKeys := append([]string(nil), repo.jobs[bad.Hash()].Keys...)
			if phase == "orphaned" {
				objects.kb[bad.Prefix()+"abandoned/meeting.md"] = []byte("unverified interrupted attempt")
			}
			outage := errors.New("AccessDenied/KMS: persistent synthetic read failure")
			reader := &indexMemberReadFailure{IndexRepository: repo, key: bad, err: outage}
			s.repo = reader
			docs := &indexDocumentSync{indexSync: provider, statuses: map[string]string{}}
			s.ingestion = docs
			repo.sources[bad.Hash()].Fields["notes"] = "edited source"
			if err := s.Enqueue(context.Background(), bad); err != nil {
				t.Fatal(err)
			}
			reader.active = phase == "catalogue"
			if phase == "exporting" || phase == "after-put" || phase == "orphaned" {
				repo.beforeSave = func(job *model.IndexJob) {
					if job.Resource == bad && job.State == model.IndexPreparing &&
						(phase != "after-put" || len(job.PendingKeys) > 0) {
						reader.active = true
					}
				}
			}
			good := []model.IndexResource{}
			total := 6
			if phase == "catalogue" {
				total = 21
			}
			for i := 0; i < total; i++ {
				good = append(good, addIndexMeeting(repo, fmt.Sprintf("healthy-%d", i), "healthy notes"))
			}
			for tick := 0; tick < 20; tick++ {
				if repo.control.Phase == "RUNNING" {
					if phase == "finalizing-read" {
						reader.active = true
					}
					if phase == "unknown-status" {
						for _, key := range repo.jobs[bad.Hash()].Keys {
							if !strings.HasSuffix(key, ".metadata.json") {
								docs.statuses[key] = "FUTURE_UNKNOWN_STATUS"
							}
						}
					}
					provider.complete()
					if phase == "unknown-status" {
						id := repo.control.ProviderJobID
						provider.jobs[id] = IndexProviderJob{ID: id, Status: "COMPLETE", Failed: 1}
					}
				}
				if _, err := s.Tick(context.Background()); err != nil &&
					!errors.Is(err, outage) && !errors.Is(err, ErrIndexInvalid) {
					t.Fatal(err)
				}
				indexed := 0
				for _, key := range good {
					if job := repo.jobs[key.Hash()]; job != nil && job.State == model.IndexIndexed {
						indexed++
					}
				}
				if indexed == len(good) && repo.jobs[bad.Hash()].State == model.IndexFailed {
					if repo.jobs[bad.Hash()].RetryAfter <= now.UnixMilli() {
						t.Fatal("detached member did not retain backoff")
					}
					if phase != "finalizing-read" && phase != "unknown-status" {
						for _, key := range oldKeys {
							if _, ok := objects.kb[key]; !ok {
								t.Fatal("read failure deleted a previously published projection")
							}
						}
						if len(repo.jobs[bad.Hash()].PendingKeys) != 0 {
							t.Fatal("detached member left an unverified pending publication")
						}
						for key := range objects.kb {
							if strings.HasPrefix(key, bad.Prefix()) && !slices.Contains(oldKeys, key) {
								t.Fatal("abandoned unverified output reached another generation")
							}
						}
					}
					return
				}
				*now = now.Add(time.Second)
				restarted := NewIndexingService(s.repo, s.objects, s.ingestion, "assets")
				restarted.now, restarted.newID = s.now, s.newID
				s = restarted
			}
			t.Fatalf("one %s member held later generations in %s", phase, repo.control.Phase)
		})
	}
}
