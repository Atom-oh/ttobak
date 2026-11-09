package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

// breakIndexMeeting makes the selected transcript a missing spill object:
// HEAD-only reads still produce a revision, but body reads fail permanently.
func breakIndexMeeting(repo *indexMemory, key model.IndexResource) {
	repo.sources[key.Hash()].Fields["transcriptB"] = "s3://assets/transcripts/" + key.ID + "/transcriptB.txt"
}

func assertIndexControlIdle(t *testing.T, repo *indexMemory) {
	t.Helper()
	c := repo.control
	if c.Phase != "IDLE" || len(c.Batch) != 0 || c.ClientToken != "" || c.ProviderJobID != "" || c.LeaseUntil != 0 {
		t.Fatalf("control not returned to a released IDLE state: %+v", c)
	}
}

func TestIndexSyncOnlyForStagedKeysOrUnconfirmedRemovals(t *testing.T) {
	cases := []struct {
		name string
		// vectors models a provider that reports per-document state; nil keeps
		// the legacy fixture, which never confirms NOT_FOUND.
		vectors      bool
		indexFirst   bool // publish and sync a good revision before the failure
		providerFail bool // the removal sync reports a failed document
		healthy      bool // add a source that stages normally in the same batch
		documentsErr error
		wantStarts   int // Start calls from the failing tick onward
		wantCleared  bool
		retriesSync  bool // later retries cannot prove removal without a sync
	}{
		{name: "all-failed batch without removals", vectors: true, wantStarts: 0, wantCleared: true},
		{name: "failed job with real removal syncs once", vectors: true, indexFirst: true, wantStarts: 1, wantCleared: true},
		{name: "removal confirmed by document status after failed sync", vectors: true, indexFirst: true, providerFail: true, wantStarts: 1, wantCleared: true},
		{name: "mixed batch still syncs", vectors: true, healthy: true, wantStarts: 1, wantCleared: true},
		{name: "provider status error keeps removal and syncs", vectors: true, documentsErr: errors.New("status unavailable"), wantStarts: 1, wantCleared: true},
		{name: "provider never reports NOT_FOUND", vectors: false, wantStarts: 1, wantCleared: true, retriesSync: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, repo, objects, p, now := newIndexTest()
			if tc.vectors {
				p.vectors = map[string]bool{}
			}
			key := addIndexMeeting(repo, "m", "notes")
			if tc.indexFirst {
				if _, err := s.Tick(context.Background()); err != nil {
					t.Fatal(err)
				}
				finishIndex(t, s, p)
				if repo.jobs[key.Hash()].State != model.IndexIndexed || len(p.vectors) == 0 {
					t.Fatal("fixture did not index the first revision")
				}
				*now = now.Add(time.Minute)
			}
			breakIndexMeeting(repo, key)
			var healthy model.IndexResource
			if tc.healthy {
				healthy = addIndexMeeting(repo, "z-healthy", "healthy notes")
			}
			p.documentsErr = tc.documentsErr
			before := len(p.tokens)
			if _, err := s.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			p.documentsErr = nil
			job := repo.jobs[key.Hash()]
			if job.State != model.IndexFailed || len(job.Keys) != 0 || len(objects.kb) != 0 && !tc.healthy {
				t.Fatalf("failed source kept projections: %+v %v", job, objects.kb)
			}
			if got := len(p.tokens) - before; got != tc.wantStarts {
				t.Fatalf("failing tick started %d syncs, want %d", got, tc.wantStarts)
			}
			if tc.wantStarts == 0 {
				assertIndexControlIdle(t, repo)
				if len(job.RemovedKeys) != 0 {
					t.Fatalf("provider-confirmed removal left pending: %v", job.RemovedKeys)
				}
			} else {
				if len(job.RemovedKeys) == 0 && !tc.healthy {
					t.Fatal("removal awaiting the provider was dropped before the sync")
				}
				if tc.providerFail {
					p.jobs[repo.control.ProviderJobID] = IndexProviderJob{ID: repo.control.ProviderJobID, Status: "COMPLETE", Failed: 1}
					p.vectors = map[string]bool{} // removed objects are gone
					if _, err := s.Tick(context.Background()); err != nil {
						t.Fatal(err)
					}
				} else {
					finishIndex(t, s, p)
				}
				assertIndexControlIdle(t, repo)
			}
			if tc.healthy && repo.jobs[healthy.Hash()].State != model.IndexIndexed {
				t.Fatalf("healthy member not indexed: %+v", repo.jobs[healthy.Hash()])
			}
			job = repo.jobs[key.Hash()]
			if job.State != model.IndexFailed {
				t.Fatalf("removal confirmation changed failed state: %+v", job)
			}
			if cleared := len(job.RemovedKeys) == 0; cleared != tc.wantCleared {
				t.Fatalf("removal cleared=%v, want %v: %v", cleared, tc.wantCleared, job.RemovedKeys)
			}
			// Later retries after each cooldown must not resync confirmed removals.
			starts, failures := len(p.tokens), job.FailureCount
			for retry := 0; retry < 3; retry++ {
				*now = now.Add(31 * time.Minute)
				if _, err := s.Tick(context.Background()); err != nil {
					t.Fatal(err)
				}
				if repo.control.Phase != "IDLE" {
					finishIndex(t, s, p)
				}
				assertIndexControlIdle(t, repo)
			}
			retried := repo.jobs[key.Hash()]
			if retried.FailureCount <= failures {
				t.Fatal("fixture did not retry the failed source")
			}
			if !tc.retriesSync && len(p.tokens) != starts {
				t.Fatalf("retries of a failed source started %d syncs", len(p.tokens)-starts)
			}
			if tc.retriesSync && len(p.tokens) == starts {
				t.Fatal("unconfirmed removal never synced again")
			}
		})
	}
}
