package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

type knowledgeReadOutage struct {
	IndexObjects
	headErr, readErr error
}

func (p knowledgeReadOutage) HeadKnowledge(ctx context.Context, key string) (IndexObject, error) {
	if p.headErr != nil {
		return IndexObject{}, p.headErr
	}
	return p.IndexObjects.HeadKnowledge(ctx, key)
}
func (p knowledgeReadOutage) ReadKnowledge(ctx context.Context, obj IndexObject) ([]byte, error) {
	if p.readErr != nil {
		return nil, p.readErr
	}
	return p.IndexObjects.ReadKnowledge(ctx, obj)
}

func TestKnowledgeTransientReadsPreservePublishedBytesAndState(t *testing.T) {
	for _, key := range []string{"kb/owner/file.pdf", "shared/reference/file.docx"} {
		for _, phase := range []string{"catalogue-head", "prepare-read", "finalize-head"} {
			t.Run(key+"/"+phase, func(t *testing.T) {
				s, repo, store, provider, _ := newKnowledgeTest()
				resource := store.original(key, "old bytes", "v1")
				if _, err := s.Tick(context.Background()); err != nil {
					t.Fatal(err)
				}
				if phase != "finalize-head" {
					finishIndex(t, s, provider)
				} else {
					provider.complete()
				}
				previous, published := indexClone(repo.jobs[resource.Hash()]), indexClone(store.kb)
				outage := errors.New("synthetic source outage")
				wrapper := knowledgeReadOutage{IndexObjects: store, headErr: outage}
				if phase == "prepare-read" {
					store.original(key, "new bytes", "v2")
					if err := s.Enqueue(context.Background(), resource); err != nil {
						t.Fatal(err)
					}
					wrapper.headErr, wrapper.readErr = nil, outage
				}
				s.objects = wrapper
				if _, err := s.Tick(context.Background()); !errors.Is(err, outage) {
					t.Fatalf("source outage was hidden: %v", err)
				}
				if !reflect.DeepEqual(published, store.kb) {
					t.Fatal("transient source outage deleted published snapshots")
				}
				current := repo.jobs[resource.Hash()]
				if phase == "catalogue-head" {
					if current.Revision != previous.Revision || current.DesiredRevision != previous.DesiredRevision ||
						!reflect.DeepEqual(current.Keys, previous.Keys) {
						t.Fatal("catalogue retry discarded published source proof")
					}
				} else if phase != "prepare-read" && !reflect.DeepEqual(previous, current) {
					t.Fatal("catalogue/finalization outage regressed the existing source job")
				}
				s.objects = store
				if _, err := s.Tick(context.Background()); err != nil {
					t.Fatal(err)
				}
				finishIndex(t, s, provider)
				if repo.jobs[resource.Hash()].State != model.IndexIndexed {
					t.Fatal("source did not recover without a new notification")
				}
			})
		}
	}
}

type knowledgeRejectRowProof struct{ IndexRepository }

func (r knowledgeRejectRowProof) SaveIndexJob(ctx context.Context, prior, next *model.IndexJob, source *model.IndexRecord, check bool, now int64) error {
	if next.Resource.IsKnowledgeSource() && check {
		return errors.New("S3 source cannot be proved by an absent DynamoDB row")
	}
	return r.IndexRepository.SaveIndexJob(ctx, prior, next, source, check, now)
}

func TestKnowledgePartialSyncIsolatesPrivateAndSharedWithoutFakeRowProof(t *testing.T) {
	s, repo, store, provider, _ := newKnowledgeTest()
	bad := store.original("kb/owner/file.pdf", "bad parser bytes", "v1")
	good := store.original("shared/reference/file.docx", "healthy shared bytes", "v1")
	s.repo = knowledgeRejectRowProof{s.repo}
	p := &indexDocumentSync{indexSync: provider, statuses: map[string]string{}}
	s.ingestion = p
	if _, err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, key := range repo.jobs[bad.Hash()].Keys {
		if !strings.HasSuffix(key, ".metadata.json") {
			p.statuses[key] = "FAILED"
		}
	}
	id := repo.control.ProviderJobID
	provider.jobs[id] = IndexProviderJob{ID: id, Status: "COMPLETE", Failed: 1}
	if _, err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repo.jobs[bad.Hash()].State != model.IndexFailed || repo.jobs[good.Hash()].State != model.IndexIndexed {
		t.Fatal("one failed binary poisoned its healthy peer")
	}
}

func TestKnowledgeDeletionProvesOriginalVectorGoneWithoutDeletingOriginal(t *testing.T) {
	for _, key := range []string{"kb/owner/file.pdf", "shared/reference/file.docx"} {
		t.Run(key, func(t *testing.T) {
			s, repo, store, provider, now := newKnowledgeTest()
			resource := store.original(key, "source bytes", "v1")
			p := &indexDocumentSync{indexSync: provider, statuses: map[string]string{key: "INDEXED"}}
			s.ingestion = p
			if _, err := s.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			finishIndex(t, s, provider)
			// Deletion is an original-owner action, never a migration write.
			delete(store.originals, key)
			delete(store.headers, key)
			if _, err := s.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			id := repo.control.ProviderJobID
			provider.jobs[id] = IndexProviderJob{ID: id, Status: "COMPLETE", Failed: 1}
			if _, err := s.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			if repo.jobs[resource.Hash()].State == model.IndexDeleted {
				t.Fatal("snapshot deletion hid a remaining original-source vector")
			}
			for _, event := range store.events {
				if event == "delete:"+key {
					t.Fatal("migration tried to delete the original upload")
				}
			}
			*now = now.Add(indexLease)
			p.statuses[key] = "NOT_FOUND"
			if _, err := s.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			id = repo.control.ProviderJobID
			provider.jobs[id] = IndexProviderJob{ID: id, Status: "COMPLETE", Failed: 1}
			if _, err := s.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			if repo.jobs[resource.Hash()].State != model.IndexDeleted {
				t.Fatal("confirmed original/snapshot removal did not recover")
			}
		})
	}
}
