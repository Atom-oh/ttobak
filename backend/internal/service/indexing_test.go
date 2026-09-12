package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

func indexClone[T any](value T) T {
	if job, ok := any(value).(*model.IndexJob); ok {
		if job == nil {
			return value
		}
		copy := *job
		copy.Keys = append([]string(nil), job.Keys...)
		copy.PendingKeys = append([]string(nil), job.PendingKeys...)
		return any(&copy).(T)
	}
	raw, _ := json.Marshal(value)
	var copy T
	if err := json.Unmarshal(raw, &copy); err != nil {
		panic(err)
	}
	return copy
}

type indexMemory struct {
	sources      map[string]*model.IndexRecord
	jobs         map[string]*model.IndexJob
	control      *model.IndexControl
	beforeSave   func(*model.IndexJob)
	afterControl func(*model.IndexControl) error
}

func (m *indexMemory) GetIndexSource(_ context.Context, key model.IndexResource) (*model.IndexRecord, error) {
	return indexClone(m.sources[key.Hash()]), nil
}
func (m *indexMemory) GetIndexJob(_ context.Context, key model.IndexResource) (*model.IndexJob, error) {
	return indexClone(m.jobs[key.Hash()]), nil
}
func (m *indexMemory) RequestIndexResource(_ context.Context, key model.IndexResource, revision string, now int64) error {
	job := indexClone(m.jobs[key.Hash()])
	if job == nil {
		job = &model.IndexJob{Resource: key}
	}
	if revision != "" {
		if job.State == model.IndexPending && job.DesiredRevision == revision {
			return nil
		}
		if job.State == model.IndexPreparing && job.DesiredRevision == revision && job.LeaseUntil > now {
			return nil
		}
		if job.State == model.IndexFailed && job.DesiredRevision == revision && job.RetryAfter > now {
			return nil
		}
		switch job.State {
		case model.IndexIndexed, model.IndexDeleted, model.IndexWaitingSync, model.IndexWaitingSource:
			if job.Revision == revision {
				return nil
			}
		}
	}
	job.Version++
	job.State, job.DesiredRevision, job.UpdatedAt, job.RetryAfter = model.IndexPending, revision, now, 0
	m.jobs[key.Hash()] = job
	return nil
}
func (m *indexMemory) SaveIndexJob(_ context.Context, prior, next *model.IndexJob, source *model.IndexRecord, check bool, now int64) error {
	if m.beforeSave != nil {
		m.beforeSave(next)
	}
	actual := m.jobs[next.Resource.Hash()]
	if actual == nil || actual.Version != prior.Version || actual.RunID != prior.RunID {
		return repository.ErrConditionFailed
	}
	if prior.State == model.IndexPreparing {
		if next.RunID == prior.RunID && actual.LeaseUntil <= now {
			return repository.ErrConditionFailed
		}
		if next.RunID != prior.RunID && actual.LeaseUntil > now {
			return repository.ErrConditionFailed
		}
	}
	if check && !reflect.DeepEqual(source, m.sources[next.Resource.Hash()]) {
		return repository.ErrConditionFailed
	}
	next.Version = prior.Version + 1
	m.jobs[next.Resource.Hash()] = indexClone(next)
	return nil
}
func (m *indexMemory) GetIndexControl(context.Context) (*model.IndexControl, error) {
	return indexClone(m.control), nil
}
func (m *indexMemory) SaveIndexControl(_ context.Context, prior, next *model.IndexControl, now int64) error {
	if m.control.Version != prior.Version || m.control.Owner != prior.Owner {
		return repository.ErrConditionFailed
	}
	if prior.Version != 0 && ((prior.Owner == next.Owner && prior.LeaseUntil <= now) || (prior.Owner != next.Owner && prior.LeaseUntil > now)) {
		return repository.ErrConditionFailed
	}
	next.Version = prior.Version + 1
	m.control = indexClone(next)
	if m.afterControl != nil {
		return m.afterControl(next)
	}
	return nil
}
func indexPage(keys []string, cursor string, limit int32) ([]string, string) {
	sort.Strings(keys)
	start, _ := strconv.Atoi(cursor)
	end := min(start+int(limit), len(keys))
	next := ""
	if end < len(keys) {
		next = strconv.Itoa(end)
	}
	return keys[start:end], next
}
func (m *indexMemory) ScanIndexSources(_ context.Context, cursor string, limit int32) ([]model.IndexResource, string, error) {
	keys := []string{}
	for key := range m.sources {
		keys = append(keys, key)
	}
	page, next := indexPage(keys, cursor, limit)
	result := []model.IndexResource{}
	for _, key := range page {
		result = append(result, m.sources[key].Resource)
	}
	return result, next, nil
}
func (m *indexMemory) ListIndexJobs(_ context.Context, cursor string, limit int32) ([]model.IndexJob, string, error) {
	keys := []string{}
	for key := range m.jobs {
		keys = append(keys, key)
	}
	page, next := indexPage(keys, cursor, limit)
	result := []model.IndexJob{}
	for _, key := range page {
		result = append(result, *indexClone(m.jobs[key]))
	}
	return result, next, nil
}

type indexStore struct {
	assets     map[string][]byte
	heads      map[string]IndexObject
	kb         map[string][]byte
	events     []string
	failPut    bool
	failDelete bool
	beforeRead func()
}

func (p *indexStore) asset(key, body, version string) {
	p.assets[key] = []byte(body)
	p.heads[key] = IndexObject{Key: key, ETag: `"` + indexTestHash(body) + `"`, VersionID: version, Size: int64(len(body))}
}
func indexTestHash(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}
func (p *indexStore) Head(_ context.Context, key string) (IndexObject, error) {
	h, ok := p.heads[key]
	if !ok {
		return IndexObject{}, ErrIndexMissing
	}
	return indexClone(h), nil
}
func (p *indexStore) Read(_ context.Context, object IndexObject) ([]byte, error) {
	if p.beforeRead != nil {
		fn := p.beforeRead
		p.beforeRead = nil
		fn()
	}
	actual, ok := p.heads[object.Key]
	if !ok {
		return nil, ErrIndexMissing
	}
	if actual.ETag != object.ETag || actual.VersionID != object.VersionID {
		return nil, ErrIndexChanged
	}
	return append([]byte(nil), p.assets[object.Key]...), nil
}
func (p *indexStore) Put(_ context.Context, key, contentType string, body []byte) error {
	if _, exists := p.kb[key]; exists {
		return errors.New("immutable key overwritten")
	}
	p.kb[key] = append([]byte(nil), body...)
	p.events = append(p.events, "put:"+key)
	if p.failPut {
		p.failPut = false
		return ErrIndexWriteUncertain
	}
	return nil
}
func (p *indexStore) Delete(_ context.Context, key string) error {
	p.events = append(p.events, "delete:"+key)
	if p.failDelete {
		p.failDelete = false
		return errors.New("delete unavailable")
	}
	delete(p.kb, key)
	return nil
}
func (p *indexStore) List(_ context.Context, prefix string) ([]string, error) {
	keys := []string{}
	for key := range p.kb {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys, nil
}
func (p *indexStore) LegacyPage(ctx context.Context, cursor string) ([]string, string, error) {
	keys, _ := p.List(ctx, "meetings/")
	page, next := indexPage(keys, cursor, 100)
	return page, next, nil
}

type indexSync struct {
	repo       *indexMemory
	store      *indexStore
	tokens     []string
	jobs       map[string]IndexProviderJob
	byToken    map[string]string
	busy       bool
	conflict   bool
	uncertain  bool
	startCheck func()
}

func (p *indexSync) Busy(context.Context) (bool, error) { return p.busy, nil }
func (p *indexSync) Start(_ context.Context, token string) (string, error) {
	if p.repo.control.Phase != "PREPARED" || p.repo.control.ClientToken != token || len(token) < 33 {
		return "", errors.New("submission token was not persisted")
	}
	p.tokens = append(p.tokens, token)
	if p.startCheck != nil {
		p.startCheck()
	}
	if p.conflict {
		p.conflict = false
		return "", ErrIndexSyncBusy
	}
	if id := p.byToken[token]; id != "" {
		return id, nil
	}
	id := fmt.Sprintf("job-%d", len(p.jobs)+1)
	p.jobs[id] = IndexProviderJob{ID: id, Status: "IN_PROGRESS"}
	p.byToken[token] = id
	if p.uncertain {
		p.uncertain = false
		return "", errors.New("accepted response lost")
	}
	return id, nil
}
func (p *indexSync) Get(_ context.Context, id string) (IndexProviderJob, error) {
	return p.jobs[id], nil
}
func (p *indexSync) complete() {
	for id, job := range p.jobs {
		job.Status = "COMPLETE"
		p.jobs[id] = job
	}
}

func newIndexTest() (*IndexingService, *indexMemory, *indexStore, *indexSync, *time.Time) {
	repo := &indexMemory{sources: map[string]*model.IndexRecord{}, jobs: map[string]*model.IndexJob{}, control: &model.IndexControl{Phase: "IDLE"}}
	store := &indexStore{assets: map[string][]byte{}, heads: map[string]IndexObject{}, kb: map[string][]byte{}}
	provider := &indexSync{repo: repo, store: store, jobs: map[string]IndexProviderJob{}, byToken: map[string]string{}}
	svc := NewIndexingService(repo, store, provider, "assets")
	now := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	sequence := 0
	svc.newID = func() string { sequence++; return fmt.Sprintf("00000000-0000-4000-8000-%012d", sequence) }
	return svc, repo, store, provider, &now
}
func addIndexMeeting(repo *indexMemory, id, notes string) model.IndexResource {
	key, _ := model.CanonicalIndexResource("USER#owner", "MEETING#"+id)
	repo.sources[key.Hash()] = &model.IndexRecord{Resource: key, Fields: map[string]interface{}{
		"meetingId": id, "userId": "owner", "title": "Title", "notes": notes, "content": "saved summary",
		"transcriptA": "unselected A", "transcriptB": "selected B", "selectedTranscript": "B",
		"actionItems": `[{"text":"task","completed":true}]`, "updatedAt": "fixed-time",
	}}
	return key
}
func finishIndex(t *testing.T, svc *IndexingService, p *indexSync) {
	t.Helper()
	p.complete()
	if _, err := svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestIndexingWaitsForSyncAndRechecksCurrentRevision(t *testing.T) {
	s, repo, objects, p, _ := newIndexTest()
	key := addIndexMeeting(repo, "m", "current notes")
	if err := s.Enqueue(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	job := repo.jobs[key.Hash()]
	if job.State != model.IndexWaitingSync || len(p.tokens) != 1 {
		t.Fatalf("premature completion: %+v", job)
	}
	var projection string
	for object, body := range objects.kb {
		if strings.HasSuffix(object, "meeting.md") {
			projection = string(body)
		}
	}
	for _, text := range []string{"current notes", "saved summary", "selected B", `"completed":true`} {
		if !strings.Contains(projection, text) {
			t.Fatalf("missing %s", text)
		}
	}
	if strings.Contains(projection, "unselected A") {
		t.Fatal("wrong variant indexed")
	}
	repo.sources[key.Hash()].Fields["notes"] = "newer notes" // missed stream during sync
	finishIndex(t, s, p)
	if repo.jobs[key.Hash()].State == model.IndexIndexed {
		t.Fatal("old revision claimed current")
	}
	if _, err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	finishIndex(t, s, p)
	if repo.jobs[key.Hash()].State != model.IndexIndexed {
		t.Fatalf("new revision not indexed: %+v", repo.jobs[key.Hash()])
	}
	// Delayed old stream event cannot restore old source text.
	if err := s.Enqueue(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	finishIndex(t, s, p)
	for object, body := range objects.kb {
		if strings.HasSuffix(object, "meeting.md") && !strings.Contains(string(body), "newer notes") {
			t.Fatal("reordered event restored old source")
		}
	}
}

func TestIndexEnqueueDistinguishesDuplicateStreamDeliveryFromEditedSource(t *testing.T) {
	s, repo, _, _, now := newIndexTest()
	key := addIndexMeeting(repo, "m", "current notes")
	source, err := s.ReadSource(context.Background(), key, false)
	if err != nil {
		t.Fatal(err)
	}
	repo.jobs[key.Hash()] = &model.IndexJob{
		Resource: key, Version: 7, State: model.IndexPreparing,
		DesiredRevision: source.Revision, RunID: "active", LeaseUntil: now.Add(time.Minute).UnixMilli(),
	}
	if err := s.Enqueue(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if job := repo.jobs[key.Hash()]; job.Version != 7 || job.State != model.IndexPreparing {
		t.Fatalf("duplicate delivery cancelled the active worker: %+v", job)
	}
	repo.sources[key.Hash()].Fields["notes"] = "edited source"
	if err := s.Enqueue(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if job := repo.jobs[key.Hash()]; job.Version != 8 || job.State != model.IndexPending || job.DesiredRevision == source.Revision {
		t.Fatalf("source edit was lost: %+v", job)
	}
}

func TestIndexSubmissionConflictAndUncertainReplyRetainToken(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprint(conflict), func(t *testing.T) {
			s, repo, _, p, _ := newIndexTest()
			key := addIndexMeeting(repo, "m", "notes")
			p.conflict, p.uncertain = conflict, !conflict
			_, err := s.Tick(context.Background())
			if conflict && err != nil {
				t.Fatal(err)
			}
			if !conflict && err == nil {
				t.Fatal("uncertain submission hidden")
			}
			if repo.control.Phase != "PREPARED" || repo.jobs[key.Hash()].State == model.IndexIndexed {
				t.Fatal("submission treated as success")
			}
			token := repo.control.ClientToken
			if _, err := s.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(p.tokens) != 2 || p.tokens[0] != token || p.tokens[1] != token || len(p.jobs) != 1 {
				t.Fatalf("duplicate logical sync: %v", p.tokens)
			}
			finishIndex(t, s, p)
			if repo.jobs[key.Hash()].State != model.IndexIndexed {
				t.Fatal("reconciled job not complete")
			}
		})
	}
}

func TestIndexCrashPartialCleanupAndExternalSync(t *testing.T) {
	s, repo, objects, p, now := newIndexTest()
	key := addIndexMeeting(repo, "m", "notes")
	p.busy = true
	if result, err := s.Tick(context.Background()); err != nil || result.Phase != "EXTERNAL_SYNC_WAIT" || len(objects.kb) != 0 {
		t.Fatalf("external sync not respected: %+v %v", result, err)
	}
	p.busy = false
	objects.failPut = true
	if _, err := s.Tick(context.Background()); !errors.Is(err, ErrIndexWriteUncertain) {
		t.Fatalf("unknown write not retained: %v", err)
	}
	partial := []string{}
	for k := range objects.kb {
		partial = append(partial, k)
	}
	if len(partial) != 1 || len(repo.jobs[key.Hash()].PendingKeys) == 0 || len(p.tokens) != 0 {
		t.Fatal("partial write not durably tracked")
	}
	if result, err := s.Tick(context.Background()); err != nil || result.Phase != "LEASE_WAIT" {
		t.Fatalf("uncertain writer not quarantined: %+v %v", result, err)
	}
	*now = now.Add(indexLease + time.Second)
	p.startCheck = func() {
		for _, key := range partial {
			if _, ok := objects.kb[key]; ok {
				t.Error("sync started before partial cleanup")
			}
		}
	}
	if _, err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	finishIndex(t, s, p)
	if repo.jobs[key.Hash()].State != model.IndexIndexed {
		t.Fatal("crash recovery failed")
	}
}

func TestIndexDeletionAndFailedCleanupCannotReportDeleted(t *testing.T) {
	s, repo, objects, p, now := newIndexTest()
	key := addIndexMeeting(repo, "m", "notes")
	if _, err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	finishIndex(t, s, p)
	delete(repo.sources, key.Hash()) // no stream notification
	objects.kb["meetings/owner/m.md"] = []byte("obsolete legacy")
	objects.failDelete = true
	starts := len(p.tokens)
	if _, err := s.Tick(context.Background()); err == nil {
		t.Fatal("cleanup failure hidden")
	}
	if len(p.tokens) != starts || repo.jobs[key.Hash()].State == model.IndexDeleted {
		t.Fatal("cleanup failure marked deleted")
	}
	*now = now.Add(time.Minute)
	if _, err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(objects.kb) != 0 || repo.jobs[key.Hash()].State == model.IndexDeleted {
		t.Fatal("deletion not gated by sync")
	}
	finishIndex(t, s, p)
	if repo.jobs[key.Hash()].State != model.IndexDeleted {
		t.Fatal("deletion not completed")
	}
	// A late legacy writer resurrects an object; a later tick must remove it.
	objects.kb["meetings/owner/m.md"] = []byte("late stale export")
	if _, err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(objects.kb) != 0 {
		t.Fatal("legacy orphan survived")
	}
	finishIndex(t, s, p)
}

func TestIndexCurrentSourceConditionWinsAfterFinalRead(t *testing.T) {
	s, repo, _, p, _ := newIndexTest()
	key := addIndexMeeting(repo, "m", "notes")
	if _, err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	repo.beforeSave = func(next *model.IndexJob) {
		if next.State == model.IndexIndexed {
			repo.sources[key.Hash()].Fields["notes"] = "concurrent correction"
		}
	}
	finishIndex(t, s, p)
	if repo.jobs[key.Hash()].State == model.IndexIndexed {
		t.Fatal("source CAS missing at terminal transition")
	}
}

func TestIndexBackfillPaginatesAndDoesNotSucceedPartialIngestion(t *testing.T) {
	s, repo, _, p, now := newIndexTest()
	for i := 0; i < 29; i++ {
		addIndexMeeting(repo, fmt.Sprintf("m%d", i), "notes")
	}
	for i := 0; i < 40; i++ {
		if _, err := s.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		p.complete()
		*now = now.Add(time.Minute)
	}
	if len(repo.jobs) != 29 {
		t.Fatalf("backfill skipped a page: %d", len(repo.jobs))
	}
	for _, job := range repo.jobs {
		if job.State != model.IndexIndexed {
			t.Fatalf("unprocessed job: %+v", job)
		}
	}
	key := addIndexMeeting(repo, "failed", "notes")
	if _, err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Cursor can require another pass before selecting the new key.
	for attempt := 0; repo.jobs[key.Hash()] == nil || repo.jobs[key.Hash()].State != model.IndexWaitingSync; attempt++ {
		if attempt == 40 {
			t.Fatal("new resource never reached WAITING_SYNC")
		}
		p.complete()
		*now = now.Add(time.Minute)
		if _, err := s.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	id := repo.control.ProviderJobID
	p.jobs[id] = IndexProviderJob{ID: id, Status: "COMPLETE", Failed: 1}
	if _, err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repo.jobs[key.Hash()].State != model.IndexFailed {
		t.Fatal("partial ingestion marked indexed")
	}
}

func TestIndexDuplicateDuringPublishCannotReplaceNewSource(t *testing.T) {
	s, repo, objects, p, _ := newIndexTest()
	key := addIndexMeeting(repo, "m", "old notes")
	repo.beforeSave = func(next *model.IndexJob) {
		if next.State == model.IndexWaitingSync {
			repo.beforeSave = nil
			repo.sources[key.Hash()].Fields["notes"] = "new notes"
			if err := s.Enqueue(context.Background(), key); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := s.Tick(context.Background()); !errors.Is(err, repository.ErrConditionFailed) {
		t.Fatalf("stale stage ignored source/event CAS: %v", err)
	}
	if len(p.tokens) != 0 {
		t.Fatal("synced a rejected stage")
	}
	stale := repo.jobs[key.Hash()].PendingKeys
	if len(stale) == 0 {
		t.Fatal("stream enqueue discarded pending projection ledger")
	}
	if _, err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, old := range stale {
		if _, exists := objects.kb[old]; exists {
			t.Fatal("rejected generation was not removed before sync")
		}
	}
	finishIndex(t, s, p)
	if repo.jobs[key.Hash()].State != model.IndexIndexed {
		t.Fatal("new source did not recover")
	}
}

func TestIndexLostControlReplyRecoversAcceptedJobWithoutResubmission(t *testing.T) {
	s, repo, _, p, now := newIndexTest()
	key := addIndexMeeting(repo, "m", "notes")
	repo.afterControl = func(next *model.IndexControl) error {
		if next.Phase == "RUNNING" {
			repo.afterControl = nil
			return errors.New("DynamoDB response lost after commit")
		}
		return nil
	}
	if _, err := s.Tick(context.Background()); err == nil {
		t.Fatal("lost database reply hidden")
	}
	if repo.control.Phase != "RUNNING" {
		t.Fatal("accepted job not persisted in crash fixture")
	}
	if result, err := s.Tick(context.Background()); err != nil || result.Phase != "LEASE_WAIT" {
		t.Fatalf("another invocation ignored lease: %+v %v", result, err)
	}
	*now = now.Add(indexLease + time.Second)
	finishIndex(t, s, p)
	if len(p.tokens) != 1 || repo.jobs[key.Hash()].State != model.IndexIndexed {
		t.Fatal("accepted job was lost or submitted twice after crash")
	}
}

func TestIndexSourceReadFailureRemovesOldContentBeforeSync(t *testing.T) {
	s, repo, objects, p, now := newIndexTest()
	key := addIndexMeeting(repo, "m", "notes")
	if _, err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	finishIndex(t, s, p)
	repo.sources[key.Hash()].Fields["transcriptB"] = "s3://assets/transcripts/m/transcriptB.txt"
	p.startCheck = func() {
		if len(objects.kb) != 0 {
			t.Fatal("failed source retained old content at sync")
		}
	}
	if _, err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	finishIndex(t, s, p)
	job := repo.jobs[key.Hash()]
	if job.State != model.IndexFailed || job.ErrorCode != "SOURCE_UNAVAILABLE" {
		t.Fatalf("unreadable source became successful empty projection: %+v", job)
	}
	status, err := s.Status(context.Background(), key)
	if err != nil || status.State != model.IndexFailed || status.ErrorCode != "SOURCE_UNAVAILABLE" {
		t.Fatalf("current source failure hidden by old exported revision: %+v %v", status, err)
	}
	starts := len(p.tokens)
	if _, err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(p.tokens) != starts {
		t.Fatal("backfill bypassed failed-source retry cooldown")
	}
	*now = now.Add(time.Minute)
	if _, err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(p.tokens) != starts+1 {
		t.Fatal("failed source never retried after cooldown")
	}
}

func TestIndexRecreationAfterDeletionReadCannotMarkDeleted(t *testing.T) {
	s, repo, _, p, _ := newIndexTest()
	key := addIndexMeeting(repo, "m", "notes")
	if _, err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	finishIndex(t, s, p)
	delete(repo.sources, key.Hash())
	if _, err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	repo.beforeSave = func(next *model.IndexJob) {
		if next.State == model.IndexDeleted {
			repo.beforeSave = nil
			addIndexMeeting(repo, "m", "recreated notes")
		}
	}
	finishIndex(t, s, p)
	if repo.jobs[key.Hash()].State == model.IndexDeleted {
		t.Fatal("recreated source was marked deleted")
	}
}
