package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

// Existing canonical-only fixtures expose an empty legacy object catalog.
func (p *indexStore) KnowledgeBucket() string { return "knowledge" }
func (p *indexStore) KnowledgePage(context.Context, string, string) ([]string, string, error) {
	return nil, "", nil
}
func (p *indexStore) HeadKnowledge(context.Context, string) (IndexObject, error) {
	return IndexObject{}, ErrIndexMissing
}
func (p *indexStore) ReadKnowledge(context.Context, IndexObject) ([]byte, error) {
	return nil, ErrIndexMissing
}

type knowledgeFixture struct {
	*indexStore
	originals    map[string][]byte
	headers      map[string]IndexObject
	contentTypes map[string]string
	pageSize     int
	readHook     func()
	afterPut     func(string) error
}

func newKnowledgeTest() (*IndexingService, *indexMemory, *knowledgeFixture, *indexSync, *time.Time) {
	service, repo, store, sync, now := newIndexTest()
	objects := &knowledgeFixture{indexStore: store, originals: map[string][]byte{},
		headers: map[string]IndexObject{}, contentTypes: map[string]string{}, pageSize: 2}
	service.objects = objects
	return service, repo, objects, sync, now
}
func (p *knowledgeFixture) original(key, text, version string) model.IndexResource {
	p.originals[key] = []byte(text)
	p.headers[key] = IndexObject{Key: key, ETag: `"` + indexTestHash(text) + `"`, VersionID: version,
		Size: int64(len(text)), ContentType: "application/source-type"}
	resource, _ := model.KnowledgeIndexResource(key)
	return resource
}
func (p *knowledgeFixture) HeadKnowledge(_ context.Context, key string) (IndexObject, error) {
	if object, exists := p.headers[key]; exists {
		return object, nil
	}
	return IndexObject{}, ErrIndexMissing
}
func (p *knowledgeFixture) ReadKnowledge(_ context.Context, expected IndexObject) ([]byte, error) {
	if p.readHook != nil {
		fn := p.readHook
		p.readHook = nil
		fn()
	}
	actual, exists := p.headers[expected.Key]
	if !exists {
		return nil, ErrIndexMissing
	}
	if actual.ETag != expected.ETag || actual.VersionID != expected.VersionID || actual.Size != expected.Size {
		return nil, ErrIndexChanged
	}
	return append([]byte(nil), p.originals[expected.Key]...), nil
}
func (p *knowledgeFixture) KnowledgePage(_ context.Context, prefix, cursor string) ([]string, string, error) {
	keys := []string{}
	for key := range p.originals {
		if strings.HasPrefix(key, prefix) && key > cursor {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if len(keys) > p.pageSize {
		return keys[:p.pageSize], keys[p.pageSize-1], nil
	}
	return keys, "", nil
}
func (p *knowledgeFixture) Put(ctx context.Context, key, contentType string, body []byte) error {
	if strings.HasPrefix(key, "kb/") || strings.HasPrefix(key, "shared/") {
		return errors.New("worker attempted to modify an original")
	}
	p.contentTypes[key] = contentType
	if err := p.indexStore.Put(ctx, key, contentType, body); err != nil {
		return err
	}
	if p.afterPut != nil {
		return p.afterPut(key)
	}
	return nil
}

func TestKnowledgeAutomaticBackfillPublishesActualBytesAndCurrentMetadata(t *testing.T) {
	service, repo, store, sync, _ := newKnowledgeTest()
	private := store.original("kb/owner/file.pdf", "%PDF-1.4\nCURRENT_PRIVATE_BYTES", "version-1")
	shared := store.original("shared/reference/file.docx", "PK\x03\x04CURRENT_SHARED_BYTES", "version-2")
	// Canonical and S3-only resources share one coalesced provider generation.
	addIndexMeeting(repo, "meeting", "current notes")
	if _, err := service.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, resource := range []model.IndexResource{private, shared} {
		job := repo.jobs[resource.Hash()]
		if job == nil || job.State != model.IndexWaitingSync {
			t.Fatalf("automatic catalog did not stage the source: %+v", job)
		}
		if _, exists := repo.sources[resource.Hash()]; exists {
			t.Fatal("migration fabricated a canonical document row")
		}
		var found bool
		for _, key := range job.Keys {
			if strings.HasSuffix(key, ".metadata.json") {
				var envelope struct {
					MetadataAttributes map[string]interface{} `json:"metadataAttributes"`
				}
				if err := json.Unmarshal(store.kb[key], &envelope); err != nil {
					t.Fatal(err)
				}
				meta := envelope.MetadataAttributes
				if meta["sourceKey"] != resource.SourceKey || meta["sourceBucket"] != "knowledge" ||
					meta["sourceRevision"] != job.Revision || meta["sourceETag"] != store.headers[resource.SourceKey].ETag ||
					meta["sourceVersionId"] != store.headers[resource.SourceKey].VersionID ||
					meta["resourceId"] != resource.ID || meta["indexRunId"] != job.RunID {
					t.Fatalf("snapshot provenance mismatch: %+v", meta)
				}
				if resource.Kind == model.IndexManualKind {
					if meta["ownerId"] != "owner" || meta["indexSchema"] != "manual-kb-v1" {
						t.Fatal("private visibility lost")
					}
				} else if meta["visibility"] != "authenticated-shared" || meta["ownerId"] != nil ||
					meta["indexSchema"] != "shared-kb-v1" {
					t.Fatal("shared visibility fabricated an owner or public grant")
				}
				continue
			}
			found = true
			if string(store.kb[key]) != string(store.originals[resource.SourceKey]) ||
				store.contentTypes[key] != "application/source-type" ||
				!strings.Contains(key, "/"+job.Revision+"/"+job.RunID+"/document.") {
				t.Fatalf("not a new exact immutable copy: %s", key)
			}
		}
		if !found {
			t.Fatal("metadata was written without source bytes")
		}
	}
	if len(sync.tokens) != 1 || len(store.originals) != 2 {
		t.Fatal("separate sync or original mutation")
	}
	finishIndex(t, service, sync)
	for _, resource := range []model.IndexResource{private, shared} {
		if repo.jobs[resource.Hash()].State != model.IndexIndexed {
			t.Fatalf("successful current revision not indexed: %+v", repo.jobs[resource.Hash()])
		}
	}
}

func TestKnowledgeOverwriteDeletionAndLateCompletionCannotRestoreOldSource(t *testing.T) {
	service, repo, store, sync, _ := newKnowledgeTest()
	key := store.original("kb/owner/file.pdf", "OLD_BYTES", "v1")
	if _, err := service.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repo.jobs[key.Hash()] == nil {
		t.Fatal("manual source was not discovered")
	}
	oldKeys := append([]string(nil), repo.jobs[key.Hash()].Keys...)
	store.original(key.SourceKey, "NEW_BYTES", "v2") // no DynamoDB stream event
	finishIndex(t, service, sync)
	if repo.jobs[key.Hash()].State == model.IndexIndexed {
		t.Fatal("stale source marked indexed")
	}
	if _, err := service.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, old := range oldKeys {
		if _, exists := store.kb[old]; exists {
			t.Fatal("obsolete snapshot survived before next sync")
		}
	}
	finishIndex(t, service, sync)
	delete(store.originals, key.SourceKey)
	delete(store.headers, key.SourceKey)
	starts := len(sync.tokens)
	store.failDelete = true
	if _, err := service.Tick(context.Background()); err == nil {
		t.Fatal("cleanup error hidden")
	}
	if len(sync.tokens) != starts || repo.jobs[key.Hash()].State == model.IndexDeleted {
		t.Fatal("failed cleanup reported successful deletion")
	}
	if _, err := service.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.kb) != 0 || repo.jobs[key.Hash()].State == model.IndexDeleted {
		t.Fatal("deletion was not gated by cleanup and sync")
	}
	finishIndex(t, service, sync)
	if repo.jobs[key.Hash()].State != model.IndexDeleted {
		t.Fatal("known-source deletion not reconciled")
	}
}

func TestKnowledgeCatalogPaginationAndFailureCooldownDoNotStarveLaterSources(t *testing.T) {
	service, repo, store, sync, now := newKnowledgeTest()
	store.pageSize = 2
	// These occupy catalog pages but must not turn into migration jobs.
	store.original("kb/owner/000.md", "text stays on its existing path", "v0")
	store.original("kb/owner/001.pdf.metadata.json", "metadata", "v0")
	var keys []model.IndexResource
	for i := 0; i < 9; i++ {
		keys = append(keys, store.original(fmt.Sprintf("kb/owner/%02d.pdf", i+2), "PDF bytes", "v1"))
	}
	bad := store.original("shared/bad.pptx", "unsupported original retained", "v1")
	for i := 0; i < 45; i++ {
		if _, err := service.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		sync.complete()
		*now = now.Add(time.Minute)
	}
	for _, key := range keys {
		if job := repo.jobs[key.Hash()]; job == nil || job.State != model.IndexIndexed {
			t.Fatalf("catalog/failure starved a source: %+v", job)
		}
	}
	job := repo.jobs[bad.Hash()]
	if job == nil || job.State != model.IndexFailed || job.ErrorCode != "UNSUPPORTED_FILE" ||
		job.FailureCount < 2 || job.RetryAfter <= job.UpdatedAt {
		t.Fatalf("unsupported source lacks durable backoff/failure: %+v", job)
	}
	if len(repo.jobs) != len(keys)+1 {
		t.Fatal("metadata/plain text was migrated")
	}
	if string(store.originals[bad.SourceKey]) != "unsupported original retained" {
		t.Fatal("failed original modified")
	}
}

func TestKnowledgeRevisionMatchesQAContractVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/knowledge-revisions.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		IndexSchema, SourceBucket, SourceKey, SourceETag, SourceVersionID, ResourceID, SourceRevision string
		SourceSize                                                                                    int64
	}
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, vector := range vectors {
		resource, ok := model.KnowledgeIndexResource(vector.SourceKey)
		if !ok || resource.ID != vector.ResourceID {
			t.Fatal("source key hash drifted")
		}
		object := IndexObject{Key: vector.SourceKey, ETag: vector.SourceETag,
			VersionID: vector.SourceVersionID, Size: vector.SourceSize}
		got := KnowledgeSourceRevision(vector.IndexSchema, vector.SourceBucket, vector.SourceKey, object)
		if got != vector.SourceRevision {
			t.Fatalf("QA revision drifted: %s != %s", got, vector.SourceRevision)
		}
	}
}

func TestKnowledgeCrashCleanupAndUncertainSubmissionUseOneCoordinator(t *testing.T) {
	service, repo, store, sync, now := newKnowledgeTest()
	key := store.original("shared/reports/file.pdf", "original v1", "v1")
	sync.busy = true
	if result, err := service.Tick(context.Background()); err != nil || result.Phase != "EXTERNAL_SYNC_WAIT" {
		t.Fatalf("external sync not respected: %+v %v", result, err)
	}
	if len(store.kb) != 0 {
		t.Fatal("published during active external sync")
	}
	sync.busy, store.failPut = false, true
	if _, err := service.Tick(context.Background()); !errors.Is(err, ErrIndexWriteUncertain) {
		t.Fatalf("unknown upload outcome hidden: %v", err)
	}
	partial := append([]string(nil), repo.jobs[key.Hash()].PendingKeys...)
	if len(partial) != 2 || len(sync.tokens) != 0 {
		t.Fatal("upload intent was not durable")
	}
	store.original(key.SourceKey, "original v2", "v2")
	if result, err := service.Tick(context.Background()); err != nil || result.Phase != "LEASE_WAIT" {
		t.Fatalf("uncertain old writer was not quarantined: %+v %v", result, err)
	}
	*now = now.Add(indexLease + time.Second)
	sync.conflict = true
	sync.startCheck = func() {
		for _, stale := range partial {
			if _, present := store.kb[stale]; present {
				t.Error("sync started before partial cleanup")
			}
		}
	}
	if _, err := service.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	token := repo.control.ClientToken
	sync.uncertain = true
	if _, err := service.Tick(context.Background()); err == nil {
		t.Fatal("uncertain submission hidden")
	}
	if _, err := service.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sync.jobs) != 1 || len(sync.tokens) != 3 {
		t.Fatalf("duplicate logical sync: %v", sync.tokens)
	}
	for _, used := range sync.tokens {
		if used != token {
			t.Fatal("submission token changed during recovery")
		}
	}
	finishIndex(t, service, sync)
	if repo.jobs[key.Hash()].State != model.IndexIndexed {
		t.Fatal("current copy failed to recover")
	}
	for name, body := range store.kb {
		if !strings.HasSuffix(name, ".metadata.json") && string(body) != "original v2" {
			t.Fatal("old copied bytes survived recovery")
		}
	}
}

func TestKnowledgeReadRaceAndProviderFailureNeverClaimSuccess(t *testing.T) {
	service, repo, store, sync, now := newKnowledgeTest()
	key := store.original("kb/owner/file.pdf", "v1 bytes", "v1")
	store.readHook = func() { store.original(key.SourceKey, "v2 bytes", "v2") }
	if _, err := service.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if job := repo.jobs[key.Hash()]; job.State != model.IndexFailed || job.ErrorCode != "SOURCE_CHANGED" {
		t.Fatalf("unpinned read accepted: %+v", job)
	}
	if len(store.kb) != 0 {
		t.Fatal("mixed source version was published")
	}
	finishIndex(t, service, sync)
	*now = now.Add(time.Minute)
	if _, err := service.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	id := repo.control.ProviderJobID
	sync.jobs[id] = IndexProviderJob{ID: id, Status: "COMPLETE", Failed: 1}
	if _, err := service.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if job := repo.jobs[key.Hash()]; job.State != model.IndexFailed || job.ErrorCode != "INGESTION_FAILED" {
		t.Fatalf("partial provider failure became INDEXED: %+v", job)
	}
}

func TestKnowledgeChangingSourceCleanupDoesNotBlockStableBatchMembers(t *testing.T) {
	service, repo, store, sync, _ := newKnowledgeTest()
	store.pageSize = 100
	keys := make([]model.IndexResource, 7)
	for i := range keys {
		keys[i] = store.original(fmt.Sprintf("kb/owner/changing-%d.pdf", i), "initial bytes", "v1")
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Hash() < keys[j].Hash() })
	hot, changes := keys[0], 0
	store.afterPut = func(key string) error {
		if strings.HasPrefix(key, hot.Prefix()) && strings.HasSuffix(key, "/document.pdf") {
			changes++
			store.original(hot.SourceKey, fmt.Sprintf("changed %d", changes), fmt.Sprintf("v%d", changes+1))
		}
		return nil
	}
	sync.startCheck = func() {
		for key := range store.kb {
			if strings.HasPrefix(key, hot.Prefix()) {
				t.Error("changing source snapshots survived into provider sync")
			}
		}
	}
	for cycle := 0; cycle < 20; cycle++ {
		if _, err := service.Tick(context.Background()); err != nil { t.Fatal(err) }
		finishIndex(t, service, sync)
		indexed := 0
		for _, key := range keys[1:] {
			if job := repo.jobs[key.Hash()]; job != nil && job.State == model.IndexIndexed {
				indexed++
			}
		}
		if indexed == 6 {
			if changes == 0 || repo.jobs[hot.Hash()].State == model.IndexIndexed {
				t.Fatal("changing source was not exercised or was falsely indexed")
			}
			if repo.jobs[hot.Hash()].ErrorCode != "SOURCE_CHANGING" {
				t.Fatal("changing source did not retain explicit failure status")
			}
			return
		}
	}
	t.Fatal("one changing S3 original blocked six stable resources")
}

func TestKnowledgeUncertainWriteTakesPrecedenceOverChangedSentinel(t *testing.T) {
	service, repo, store, sync, now := newKnowledgeTest()
	store.original("shared/uncertain.pdf", "source bytes", "v1")
	store.afterPut = func(string) error { return errors.Join(ErrIndexWriteUncertain, ErrIndexChanged) }
	if _, err := service.Tick(context.Background()); !errors.Is(err, ErrIndexWriteUncertain) {
		t.Fatalf("uncertainty was treated as definitive: %v", err)
	}
	if repo.control.LeaseUntil <= now.UnixMilli() || len(sync.tokens) != 0 {
		t.Fatal("uncertain write did not freeze publication")
	}
	for _, event := range store.events {
		if strings.HasPrefix(event, "delete:") {
			t.Fatal("uncertain write was cleaned while it could still arrive")
		}
	}
	if result, err := service.Tick(context.Background()); err != nil || result.Phase != "LEASE_WAIT" {
		t.Fatalf("successor bypassed uncertain writer lease: %+v %v", result, err)
	}
}
