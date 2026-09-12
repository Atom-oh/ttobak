package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

type indexReadOutage struct {
	IndexObjects
	headErr, readErr error
}

func (p indexReadOutage) Head(ctx context.Context, key string) (IndexObject, error) {
	if p.headErr != nil {
		return IndexObject{}, p.headErr
	}
	return p.IndexObjects.Head(ctx, key)
}
func (p indexReadOutage) Read(ctx context.Context, object IndexObject) ([]byte, error) {
	if p.readErr != nil {
		return nil, p.readErr
	}
	return p.IndexObjects.Read(ctx, object)
}

type indexWithoutSourceScan struct{ IndexRepository }

func (r indexWithoutSourceScan) ScanIndexSources(context.Context, string, int32) ([]model.IndexResource, string, error) {
	return nil, "", nil
}

func TestIndexTransientReadsPreservePublishedProjections(t *testing.T) {
	for _, phase := range []string{"source-scan", "known-job", "prepare-get", "reuse-head", "finish"} {
		t.Run(phase, func(t *testing.T) {
			s, repo, objects, provider, _ := newIndexTest()
			key := addIndexMeeting(repo, "m", "current notes")
			objects.asset("transcripts/m/transcriptB.txt", "current transcript", "v1")
			repo.sources[key.Hash()].Fields["transcriptB"] = "s3://assets/transcripts/m/transcriptB.txt"
			if _, err := s.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			if phase != "reuse-head" && phase != "finish" {
				finishIndex(t, s, provider)
			}
			published := indexClone(objects.kb)
			previous := indexClone(repo.jobs[key.Hash()])
			starts := len(provider.tokens)
			outage := errors.New("synthetic transient storage outage")
			var err error
			switch phase {
			case "source-scan", "known-job":
				repo.readError = outage
				if phase == "known-job" {
					s.repo = indexWithoutSourceScan{s.repo}
				}
				_, err = s.Tick(context.Background())
			case "prepare-get":
				repo.sources[key.Hash()].Fields["notes"] = "saved correction"
				if err := s.Enqueue(context.Background(), key); err != nil {
					t.Fatal(err)
				}
				s.objects = indexReadOutage{IndexObjects: objects, readErr: outage}
				_, err = s.Tick(context.Background())
				if repo.jobs[key.Hash()].State != model.IndexPending {
					t.Error("transient preparation did not remain retryable")
				}
			case "reuse-head":
				s.objects = indexReadOutage{IndexObjects: objects, headErr: outage}
				_, err = s.prepare(context.Background(), key)
			case "finish":
				provider.complete()
				repo.readError = outage
				_, err = s.Tick(context.Background())
			}
			if !errors.Is(err, outage) {
				t.Errorf("transient outage not surfaced: %v", err)
			}
			if !reflect.DeepEqual(objects.kb, published) || len(provider.tokens) != starts {
				t.Fatal("transient source read deleted projections or submitted destructive sync")
			}
			if phase != "prepare-get" && !reflect.DeepEqual(repo.jobs[key.Hash()], previous) {
				t.Fatalf("transient read regressed durable source state: %+v", repo.jobs[key.Hash()])
			}
			// Recovery uses the same source/objects; no new stream notification.
			s.repo, s.objects, repo.readError = repo, objects, nil
			if _, err := s.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			finishIndex(t, s, provider)
			if repo.jobs[key.Hash()].State != model.IndexIndexed {
				t.Fatal("source did not recover after the transient outage")
			}
		})
	}
}

type indexDocumentSync struct {
	*indexSync
	statuses    map[string]string
	err         error
	onDocuments func()
}

func (p *indexDocumentSync) Documents(_ context.Context, keys []string) (map[string]string, error) {
	if p.err != nil {
		return nil, p.err
	}
	if p.onDocuments != nil {
		p.onDocuments()
	}
	result := map[string]string{}
	for _, key := range keys {
		status := p.statuses[key]
		if status == "" {
			if _, exists := p.store.kb[key]; exists {
				status = "INDEXED"
			} else {
				status = "NOT_FOUND"
			}
		}
		result[key] = status
	}
	return result, nil
}

func TestIndexPartialProviderFailureDoesNotPoisonHealthyMembers(t *testing.T) {
	s, repo, objects, provider, now := newIndexTest()
	good := addIndexMeeting(repo, "good", "healthy notes")
	bad := addIndexMeeting(repo, "bad", "bad provider parse")
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
	if repo.jobs[good.Hash()].State != model.IndexIndexed || repo.jobs[bad.Hash()].State != model.IndexFailed {
		t.Fatalf("healthy member inherited another document's failure: good=%s bad=%s",
			repo.jobs[good.Hash()].State, repo.jobs[bad.Hash()].State)
	}
	goodKeys := append([]string(nil), repo.jobs[good.Hash()].Keys...)
	// The failed immutable projection can remain in the source until retry.
	// Its repeated failure must not hold the next healthy member in lockstep.
	later := addIndexMeeting(repo, "later", "more healthy notes")
	*now = now.Add(2 * time.Minute)
	if _, err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, key := range repo.jobs[bad.Hash()].Keys {
		p.statuses[key] = "FAILED"
	}
	id = repo.control.ProviderJobID
	provider.jobs[id] = IndexProviderJob{ID: id, Status: "COMPLETE", Failed: 1}
	if _, err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repo.jobs[later.Hash()].State != model.IndexIndexed {
		t.Fatal("poison document blocked a later healthy generation")
	}
	for _, key := range goodKeys {
		if _, exists := objects.kb[key]; !exists {
			t.Fatal("healthy projection was needlessly removed")
		}
	}
}

func TestIndexPartialSyncVerifiesDeletionAndRetainsRemovalProofAcrossCrash(t *testing.T) {
	s, repo, objects, provider, _ := newIndexTest()
	key := addIndexMeeting(repo, "deleted", "old notes")
	p := &indexDocumentSync{indexSync: provider, statuses: map[string]string{}}
	s.ingestion = p
	if _, err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	finishIndex(t, s, provider)
	old := append([]string(nil), repo.jobs[key.Hash()].Keys...)
	delete(repo.sources, key.Hash())
	objects.failDelete = true
	if _, err := s.Tick(context.Background()); err == nil {
		t.Fatal("fixture did not interrupt cleanup")
	}
	if len(repo.jobs[key.Hash()].RemovedKeys) != 2 {
		t.Fatal("deletion identifiers were not persisted before the first S3 delete")
	}
	if _, err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	id := repo.control.ProviderJobID
	provider.jobs[id] = IndexProviderJob{ID: id, Status: "COMPLETE", Failed: 1}
	// An unrelated poison file cannot prevent verified deletion of this member.
	if _, err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if job := repo.jobs[key.Hash()]; job.State != model.IndexDeleted || len(job.RemovedKeys) != 0 {
		t.Fatalf("verified deletion coupled to unrelated ingestion failure: %+v", job)
	}
	for _, object := range old {
		if _, ok := objects.kb[object]; ok {
			t.Fatal("source deletion retained a projection")
		}
	}
}

func TestIndexPartialSyncCannotAssumeAbsentLegacyObjectMeansAbsentVector(t *testing.T) {
	s, repo, _, provider, _ := newIndexTest()
	key := addIndexMeeting(repo, "legacy", "notes")
	if err := s.Enqueue(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	delete(repo.sources, key.Hash())
	legacy := legacyIndexKeys(key)[0]
	// The legacy S3 export is already gone, but its deletion failed in the
	// provider. It never appeared in this new worker's key inventory.
	p := &indexDocumentSync{indexSync: provider, statuses: map[string]string{legacy: "INDEXED"}}
	s.ingestion = p
	if _, err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	id := repo.control.ProviderJobID
	provider.jobs[id] = IndexProviderJob{ID: id, Status: "COMPLETE", Failed: 1}
	if _, err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repo.jobs[key.Hash()].State == model.IndexDeleted {
		t.Fatal("S3 absence became proof of vector deletion after a partially failed sync")
	}
	if len(repo.jobs[key.Hash()].RemovedKeys) != 1 || repo.jobs[key.Hash()].RemovedKeys[0] != legacy {
		t.Fatal("unconfirmed legacy deletion was not retained for a later sync")
	}
}

func TestIndexDocumentStatusWaitDoesNotRegressOtherMembersAndSourceIsRechecked(t *testing.T) {
	s, repo, _, provider, _ := newIndexTest()
	waiting := addIndexMeeting(repo, "waiting", "notes")
	healthy := addIndexMeeting(repo, "healthy", "notes")
	p := &indexDocumentSync{indexSync: provider, statuses: map[string]string{}}
	s.ingestion = p
	if _, err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitKey := repo.jobs[waiting.Hash()].Keys[0]
	p.statuses[waitKey] = "IN_PROGRESS"
	id := repo.control.ProviderJobID
	provider.jobs[id] = IndexProviderJob{ID: id, Status: "COMPLETE", Failed: 1}
	if _, err := s.Tick(context.Background()); !errors.Is(err, ErrIndexSyncBusy) {
		t.Fatalf("pending document status was not surfaced: %v", err)
	}
	if repo.jobs[healthy.Hash()].State != model.IndexIndexed || repo.jobs[waiting.Hash()].State != model.IndexWaitingSync {
		t.Fatal("one unresolved member blocked or regressed its healthy peer")
	}
	p.statuses[waitKey] = "INDEXED"
	p.onDocuments = func() {
		repo.sources[waiting.Hash()].Fields["notes"] = "edited during provider status read"
	}
	if _, err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repo.jobs[waiting.Hash()].State != model.IndexPending {
		t.Fatal("per-document INDEXED bypassed fresh source proof")
	}
}

func TestIndexOnlyCompleteDocumentAndRemovalStatusesCanSucceed(t *testing.T) {
	for _, status := range []string{"INDEXED", "PARTIALLY_INDEXED", "METADATA_PARTIALLY_INDEXED",
		"METADATA_UPDATE_FAILED", "FAILED", "IGNORED", "NOT_FOUND", "STARTING", "PENDING",
		"IN_PROGRESS", "DELETING", "DELETE_IN_PROGRESS", "UNRECOGNIZED"} {
		t.Run(status, func(t *testing.T) {
			s, _, _, provider, _ := newIndexTest()
			p := &indexDocumentSync{indexSync: provider, statuses: map[string]string{"current": status, "removed": status}}
			s.ingestion = p
			indexed, _, err := s.documentsSucceeded(context.Background(), []string{"current"}, nil)
			if indexed != (status == "INDEXED") || (status == "INDEXED" && err != nil) {
				t.Fatalf("current document status %s became success=%v err=%v", status, indexed, err)
			}
			deleted, _, err := s.documentsSucceeded(context.Background(), nil, []string{"removed"})
			if deleted != (status == "NOT_FOUND") || (status == "NOT_FOUND" && err != nil) {
				t.Fatalf("removed document status %s became deletion=%v err=%v", status, deleted, err)
			}
		})
	}
}

// The source-reader prerequisite tests the revision contract independently;
// retain the worker Status integration assertion in this lifecycle suite.
func TestIndexStatusObservesReplacedDocumentBytes(t *testing.T) {
	for _, pk := range []string{"USER#owner", "ACCOUNT#team"} {
		t.Run(pk, func(t *testing.T) {
			s, repo, objects, provider, _ := newIndexTest()
			key, _ := model.CanonicalIndexResource(pk, "DOC#d")
			repo.sources[key.Hash()] = &model.IndexRecord{Resource: key, Fields: map[string]interface{}{
				"docId": "d", "accountId": "team", "fileKey": "docs/owner/file.pdf", "updatedAt": "unchanged",
			}}
			objects.asset("docs/owner/file.pdf", "old PDF bytes", "v1")
			if _, err := s.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			finishIndex(t, s, provider)
			objects.asset("docs/owner/file.pdf", "changed PDF bytes", "v2")
			status, err := s.Status(context.Background(), key)
			if err != nil || status.State != model.IndexPending {
				t.Fatalf("same updatedAt masked changed bytes: %+v %v", status, err)
			}
		})
	}
}
