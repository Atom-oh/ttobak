package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type IndexRepository interface {
	GetIndexSource(context.Context, model.IndexResource) (*model.IndexRecord, error)
	GetIndexJob(context.Context, model.IndexResource) (*model.IndexJob, error)
	RequestIndexResource(context.Context, model.IndexResource, string, int64) error
	SaveIndexJob(context.Context, *model.IndexJob, *model.IndexJob, *model.IndexRecord, bool, int64) error
	GetIndexControl(context.Context) (*model.IndexControl, error)
	SaveIndexControl(context.Context, *model.IndexControl, *model.IndexControl, int64) error
	ScanIndexSources(context.Context, string, int32) ([]model.IndexResource, string, error)
	ListIndexJobs(context.Context, string, int32) ([]model.IndexJob, string, error)
}

const indexLease = 20 * time.Minute // Longer than a Lambda invocation, including its final network operations.
const indexBatchSize = 4
const indexMemberAttempts = 3

type IndexingService struct {
	repo         IndexRepository
	objects      IndexObjects
	ingestion    IndexIngestion
	assetsBucket string
	now          func() time.Time
	newID        func() string
}

func NewIndexingService(repo IndexRepository, objects IndexObjects, ingestion IndexIngestion, assetsBucket string) *IndexingService {
	return &IndexingService{repo: repo, objects: objects, ingestion: ingestion, assetsBucket: assetsBucket, now: time.Now, newID: uuid.NewString}
}

func (s *IndexingService) ReadSource(ctx context.Context, key model.IndexResource, bodies bool) (*IndexSnapshot, error) {
	return NewIndexSourceReader(s.repo, s.objects, s.assetsBucket).ReadSource(ctx, key, bodies)
}

func (s *IndexingService) Enqueue(ctx context.Context, key model.IndexResource) error {
	canonical, ok := model.CanonicalIndexResource(key.PK, key.SK)
	if !ok || canonical != key {
		return ErrIndexInvalid
	}
	// Stream records are notifications, not source snapshots. Read the current
	// revision so duplicate delivery can coalesce with an active preparation,
	// while a real edit still invalidates that worker's conditional completion.
	source, err := s.ReadSource(ctx, key, false)
	if errors.Is(err, ErrIndexInvalid) {
		// A permanent source defect belongs in the durable job's failure
		// state, not in repeated delivery of the same stream sequence.
		return s.repo.RequestIndexResource(ctx, key, "", s.now().UnixMilli())
	}
	if err != nil {
		return err
	}
	return s.repo.RequestIndexResource(ctx, key, source.Revision, s.now().UnixMilli())
}

type IndexTickResult struct {
	Phase         string `json:"phase"`
	Resources     int    `json:"resources"`
	Failed        int    `json:"failed"`
	ProviderJobID string `json:"providerJobId,omitempty"`
}

// Tick never equates StartIngestionJob acceptance with successful indexing.
// A durable batch freezes S3 mutations while its provider sync is unresolved.
func (s *IndexingService) Tick(ctx context.Context) (result IndexTickResult, err error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	control, err := s.repo.GetIndexControl(ctx)
	if err != nil {
		return result, err
	}
	if control.LeaseUntil > s.now().UnixMilli() {
		return IndexTickResult{Phase: "LEASE_WAIT"}, nil
	}
	owned := *control
	owned.Owner, owned.LeaseUntil = s.newID(), s.now().Add(indexLease).UnixMilli()
	if err := s.repo.SaveIndexControl(ctx, control, &owned, s.now().UnixMilli()); err != nil {
		return result, err
	}
	control = &owned
	// Ambiguous uploads retain the lease: the old invocation must be dead before
	// a successor cleans its planned keys and starts a new ingestion generation.
	defer func() {
		if result.Phase == "" {
			result.Phase = control.Phase
		}
		result.ProviderJobID = control.ProviderJobID
		if errors.Is(err, ErrIndexWriteUncertain) {
			return
		}
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		released := *control
		released.LeaseUntil = 0
		if releaseErr := s.repo.SaveIndexControl(cleanup, control, &released, s.now().UnixMilli()); releaseErr != nil {
			err = errors.Join(err, releaseErr)
		}
	}()
	save := func(next model.IndexControl) error {
		if e := s.repo.SaveIndexControl(ctx, control, &next, s.now().UnixMilli()); e != nil {
			return e
		}
		*control = next
		return nil
	}
	if control.Phase == "" || control.Phase == "IDLE" {
		busy, e := s.ingestion.Busy(ctx)
		if e != nil {
			return result, e
		}
		if busy {
			result.Phase = "EXTERNAL_SYNC_WAIT"
			return result, nil
		}
		next := *control
		batch, e := s.reconcile(ctx, &next)
		if e != nil {
			return result, e
		}
		next.Batch = batch
		if len(batch) > 0 {
			next.Phase = "EXPORTING"
		} else {
			next.Phase = "IDLE"
		}
		if e := save(next); e != nil {
			return result, e
		}
	}
	if control.Phase == "EXPORTING" {
		next := *control
		next.Batch = append([]model.IndexMember(nil), control.Batch...)
		var failures error
		for i, member := range next.Batch {
			if member.Detached {
				continue
			}
			current, e := s.prepare(ctx, member.Resource)
			if e != nil {
				if errors.Is(e, ErrIndexWriteUncertain) {
					return result, e
				}
				if errors.Is(e, ErrIndexChanged) || errors.Is(e, repository.ErrConditionFailed) {
					if e := s.deferChangingSource(ctx, member.Resource); e != nil {
						return result, e
					}
					next.Batch[i] = model.IndexMember{Resource: member.Resource, Detached: true}
					result.Failed++
					continue
				}
				if errors.Is(e, ErrIndexLeaseBusy) {
					return result, e
				}
				if retryErr := s.retryPreparation(ctx, member.Resource); retryErr != nil {
					return result, errors.Join(e, retryErr)
				}
				if retryErr := s.memberFailure(ctx, &next.Batch[i], "PREPARATION_EXHAUSTED"); retryErr != nil {
					return result, errors.Join(e, retryErr)
				}
				if next.Batch[i].Detached {
					result.Failed++
				} else {
					failures = errors.Join(failures, e)
				}
				continue
			}
			next.Batch[i] = current
			result.Resources++
			if current.Revision == "" {
				result.Failed++
			}
		}
		if failures != nil {
			return result, errors.Join(failures, save(next))
		}
		next.Phase, next.ClientToken, next.ErrorCode = "PREPARED", s.newID(), ""
		if e := save(next); e != nil {
			return result, e
		}
	}
	if control.Phase == "PREPARED" {
		// Repeat the same persisted token after timeouts, conflicts or lost replies.
		id, e := s.ingestion.Start(ctx, control.ClientToken)
		next := *control
		if e != nil {
			next.ErrorCode = "SUBMISSION_UNCERTAIN"
			if errors.Is(e, ErrIndexSyncBusy) {
				next.ErrorCode = "EXTERNAL_SYNC_WAIT"
			}
			if saveErr := save(next); saveErr != nil {
				return result, errors.Join(e, saveErr)
			}
			if errors.Is(e, ErrIndexSyncBusy) {
				result.Phase = "EXTERNAL_SYNC_WAIT"
				return result, nil
			}
			return result, e
		}
		if id == "" {
			return result, fmt.Errorf("%w: ingestion reply has no job ID", ErrIndexInvalid)
		}
		next.ProviderJobID, next.Phase, next.ErrorCode = id, "RUNNING", ""
		if e := save(next); e != nil {
			return result, e
		}
		return result, nil
	}
	if control.Phase == "RUNNING" {
		job, e := s.ingestion.Get(ctx, control.ProviderJobID)
		if e != nil {
			return result, e
		}
		if job.ID != control.ProviderJobID {
			return result, ErrIndexInvalid
		}
		switch job.Status {
		case "STARTING", "IN_PROGRESS", "STOPPING":
			return result, nil
		case "COMPLETE", "FAILED", "STOPPED":
			next := *control
			next.ProviderSucceeded = job.Status == "COMPLETE" && job.Failed == 0 && len(job.FailureReasons) == 0
			next.Phase = "FINALIZING"
			if e := save(next); e != nil {
				return result, e
			}
		default:
			return result, fmt.Errorf("%w: unknown ingestion status", ErrIndexInvalid)
		}
	}
	if control.Phase == "FINALIZING" {
		next := *control
		next.Batch = append([]model.IndexMember(nil), control.Batch...)
		var failures error
		for i, member := range next.Batch {
			if member.Detached {
				continue
			}
			if e := s.finish(ctx, member, control.ProviderJobID, control.ProviderSucceeded); e != nil {
				if retryErr := s.memberFailure(ctx, &next.Batch[i], "FINALIZATION_EXHAUSTED"); retryErr != nil {
					return result, errors.Join(e, retryErr)
				}
				if next.Batch[i].Detached {
					result.Failed++
				} else {
					failures = errors.Join(failures, e)
				}
			}
		}
		if failures != nil {
			return result, errors.Join(failures, save(next))
		}
		next.Phase, next.Batch, next.ClientToken, next.ProviderJobID, next.ErrorCode = "IDLE", nil, "", "", ""
		next.ProviderSucceeded = false
		if e := save(next); e != nil {
			return result, e
		}
	}
	return result, nil
}

func (s *IndexingService) memberFailure(ctx context.Context, member *model.IndexMember, code string) error {
	member.Attempts++
	if member.Attempts < indexMemberAttempts {
		return nil
	}
	job, err := s.repo.GetIndexJob(ctx, member.Resource)
	if err != nil {
		return err
	}
	if job != nil {
		if code == "FINALIZATION_EXHAUSTED" && (job.Version != member.Version || job.RunID != member.RunID) {
			member.Detached = true
			return nil
		}
		next := *job
		// Source/status unavailability proves no deletion. Keep all published
		// keys, removal obligations and the last known source revision.
		s.failJob(&next, code)
		if err := s.repo.SaveIndexJob(ctx, job, &next, nil, false, s.now().UnixMilli()); err != nil &&
			!errors.Is(err, repository.ErrConditionFailed) {
			return err
		}
	}
	member.Detached = true
	return nil
}

func permanentIndexSourceError(err error) bool {
	return errors.Is(err, ErrIndexInvalid) || errors.Is(err, ErrIndexMissing)
}

func (s *IndexingService) deferChangingSource(ctx context.Context, key model.IndexResource) error {
	// The coordinator owns publication. These errors are definitive, unlike
	// an uncertain PUT, so obsolete staged objects can be removed safely.
	job, err := s.repo.GetIndexJob(ctx, key)
	if err != nil || job == nil {
		return err
	}
	if err := s.cleanupTracked(ctx, job, nil); err != nil {
		return err
	}
	next := *job
	s.failJob(&next, "SOURCE_CHANGING")
	next.Keys, next.PendingKeys = nil, nil
	err = s.repo.SaveIndexJob(ctx, job, &next, nil, false, s.now().UnixMilli())
	// A newer notification can retain PENDING; it must not hold this batch.
	if errors.Is(err, repository.ErrConditionFailed) {
		return nil
	}
	return err
}

func (s *IndexingService) reconcile(ctx context.Context, control *model.IndexControl) ([]model.IndexMember, error) {
	keys, cursor, err := s.repo.ScanIndexSources(ctx, control.SourceCursor, 25)
	if err != nil {
		return nil, err
	}
	for _, key := range keys {
		revision := ""
		if source, e := s.ReadSource(ctx, key, false); e == nil {
			revision = source.Revision
		} else if !permanentIndexSourceError(e) {
			if err := s.queueUnreadable(ctx, key); err != nil {
				return nil, err
			}
			continue
		}
		if err := s.repo.RequestIndexResource(ctx, key, revision, s.now().UnixMilli()); err != nil {
			return nil, err
		}
	}
	control.SourceCursor = cursor
	legacy, legacyCursor, err := s.objects.LegacyPage(ctx, control.LegacyCursor)
	if err != nil {
		return nil, err
	}
	for _, object := range legacy {
		parts := strings.Split(strings.TrimSuffix(object, ".metadata.json"), "/")
		if len(parts) != 3 || parts[0] != "meetings" || !strings.HasSuffix(parts[2], ".md") {
			continue
		}
		key, ok := model.CanonicalIndexResource("USER#"+parts[1], "MEETING#"+strings.TrimSuffix(parts[2], ".md"))
		if ok {
			// A transient read is not evidence that a legacy source is gone.
			// Known invalid sources still enter the durable cleanup path.
			if err := s.Enqueue(ctx, key); err != nil {
				if err := s.queueUnreadable(ctx, key); err != nil {
					return nil, err
				}
			}
		}
	}
	control.LegacyCursor = legacyCursor
	// Never advance past eligible jobs that did not fit this generation.
	// Reading at most a batch preserves rotation even when early jobs fail forever.
	jobs, cursor, err := s.repo.ListIndexJobs(ctx, control.JobCursor, indexBatchSize)
	if err != nil {
		return nil, err
	}
	batch := []model.IndexMember{}
	for _, job := range jobs {
		// Known jobs also detect deleted sources and same-key S3 byte replacements.
		source, readErr := s.ReadSource(ctx, job.Resource, false)
		changed := readErr != nil || source.Revision != job.Revision
		unavailable := readErr != nil && !permanentIndexSourceError(readErr)
		if unavailable {
			changed = false
		}
		extra := false
		if !changed && (job.State == model.IndexIndexed || job.State == model.IndexDeleted || job.State == model.IndexWaitingSource) {
			complete, e := s.inventoryMatches(ctx, job.Resource, job.Keys)
			if e != nil {
				unavailable = true
			} else {
				changed, extra = !complete, !complete
			}
		}
		if changed && job.State != model.IndexPreparing && job.State != model.IndexWaitingSync {
			revision := ""
			if readErr == nil && !extra {
				revision = source.Revision
			}
			if err := s.repo.RequestIndexResource(ctx, job.Resource, revision, s.now().UnixMilli()); err != nil {
				return nil, err
			}
			// Request may deliberately keep a failed job in its cooldown.
			// Use its actual state, including any concurrent stream update.
			current, err := s.repo.GetIndexJob(ctx, job.Resource)
			if err != nil {
				return nil, err
			}
			if current == nil {
				continue
			}
			job = *current
		}
		eligible := job.State == model.IndexPending || job.State == model.IndexWaitingSync ||
			(job.State == model.IndexFailed && job.RetryAfter <= s.now().UnixMilli()) ||
			(job.State == model.IndexPreparing && job.LeaseUntil <= s.now().UnixMilli()) ||
			(unavailable && (job.State == model.IndexIndexed || job.State == model.IndexDeleted || job.State == model.IndexWaitingSource))
		if eligible && len(batch) < indexBatchSize {
			batch = append(batch, model.IndexMember{Resource: job.Resource})
		}
	}
	control.JobCursor = cursor
	return batch, nil
}

func (s *IndexingService) queueUnreadable(ctx context.Context, key model.IndexResource) error {
	job, err := s.repo.GetIndexJob(ctx, key)
	if err != nil || job != nil {
		return err
	}
	return s.repo.RequestIndexResource(ctx, key, "", s.now().UnixMilli())
}

func (s *IndexingService) prepare(ctx context.Context, key model.IndexResource) (model.IndexMember, error) {
	prior, err := s.repo.GetIndexJob(ctx, key)
	if err != nil {
		return model.IndexMember{}, err
	}
	if prior == nil {
		return model.IndexMember{}, ErrIndexInvalid
	}
	if prior.State == model.IndexFailed && prior.RetryAfter > s.now().UnixMilli() {
		return model.IndexMember{Resource: key, Detached: true}, nil
	}
	if prior.State == model.IndexWaitingSync {
		current, e := s.ReadSource(ctx, key, false)
		if e != nil && !permanentIndexSourceError(e) {
			return model.IndexMember{}, e
		}
		if e == nil && current.Revision == prior.Revision {
			complete, e := s.inventoryMatches(ctx, key, prior.Keys)
			if e != nil {
				return model.IndexMember{}, e
			}
			if complete {
				return indexMember(prior), nil
			}
		}
	}
	if prior.State == model.IndexPreparing && prior.LeaseUntil > s.now().UnixMilli() {
		return model.IndexMember{}, ErrIndexLeaseBusy
	}
	job := *prior
	job.RunID, job.State, job.LeaseUntil = s.newID(), model.IndexPreparing, s.now().Add(indexLease).UnixMilli()
	if err := s.repo.SaveIndexJob(ctx, prior, &job, nil, false, s.now().UnixMilli()); err != nil {
		return model.IndexMember{}, err
	}
	source, err := s.ReadSource(ctx, key, true)
	if err != nil {
		if !permanentIndexSourceError(err) {
			// Unavailability supplies no proof that published bytes are obsolete.
			// The caller resets preparation for retry while keeping tracked keys.
			return model.IndexMember{}, err
		}
		// A confirmed missing or invalid source cannot retain stale projections.
		if cleanupErr := s.cleanupTracked(ctx, &job, nil); cleanupErr != nil {
			return model.IndexMember{}, errors.Join(err, cleanupErr)
		}
		failed := job
		s.failJob(&failed, "SOURCE_UNAVAILABLE")
		failed.Keys, failed.PendingKeys = nil, nil
		if e := s.repo.SaveIndexJob(ctx, &job, &failed, nil, false, s.now().UnixMilli()); e != nil {
			return model.IndexMember{}, e
		}
		return model.IndexMember{Resource: key, Version: failed.Version, RunID: failed.RunID}, nil
	}
	keys := []string{}
	prefix := key.Prefix() + job.RunID + "/"
	for _, part := range source.Parts {
		keys = append(keys, prefix+part.Name, prefix+part.Name+".metadata.json")
	}
	planned := job
	planned.PendingKeys, planned.Revision, planned.Outcome = keys, source.Revision, source.Outcome
	planned.DesiredRevision, planned.ErrorCode = source.Revision, source.ErrorCode
	if err := s.repo.SaveIndexJob(ctx, &job, &planned, source.Record, true, s.now().UnixMilli()); err != nil {
		return model.IndexMember{}, err
	}
	job = planned
	metadata, err := indexMetadata(key, source, job.RunID)
	if err != nil {
		return model.IndexMember{}, err
	}
	for _, part := range source.Parts {
		object := prefix + part.Name
		// Metadata first: a concurrently running external sync must not see a
		// data object before its canonical identity/provenance sidecar exists.
		if err := s.objects.Put(ctx, object+".metadata.json", "application/json", metadata); err != nil {
			return model.IndexMember{}, err
		}
		if err := s.objects.Put(ctx, object, part.ContentType, part.Body); err != nil {
			return model.IndexMember{}, err
		}
	}
	current, err := s.ReadSource(ctx, key, false)
	if err != nil {
		return model.IndexMember{}, err
	}
	if current.Revision != source.Revision {
		return model.IndexMember{}, ErrIndexChanged
	}
	if err := s.cleanupTracked(ctx, &job, keys); err != nil {
		return model.IndexMember{}, err
	}
	ready := job
	ready.Keys, ready.PendingKeys, ready.State, ready.LeaseUntil = keys, nil, model.IndexWaitingSync, 0
	ready.UpdatedAt = s.now().UnixMilli()
	if err := s.repo.SaveIndexJob(ctx, &job, &ready, current.Record, true, s.now().UnixMilli()); err != nil {
		return model.IndexMember{}, err
	}
	return indexMember(&ready), nil
}

func (s *IndexingService) retryPreparation(ctx context.Context, key model.IndexResource) error {
	job, err := s.repo.GetIndexJob(ctx, key)
	if err != nil || job == nil {
		return err
	}
	if job.State != model.IndexPreparing {
		return nil
	}
	// Only unverified outputs of this attempt are disposable. Keys are promoted
	// durably after validation, before old objects are removed.
	pendingKeys, err := s.objects.List(ctx, key.Prefix())
	if err != nil {
		return err
	}
	next := *job
	for _, key := range pendingKeys {
		if !slices.Contains(job.Keys, key) && !strings.HasSuffix(key, ".metadata.json") && !slices.Contains(next.RemovedKeys, key) {
			next.RemovedKeys = append(next.RemovedKeys, key)
		}
	}
	if err := s.repo.SaveIndexJob(ctx, job, &next, nil, false, s.now().UnixMilli()); err != nil {
		return err
	}
	for _, pending := range pendingKeys {
		if !strings.HasPrefix(pending, key.Prefix()) {
			return ErrIndexInvalid
		}
		if slices.Contains(job.Keys, pending) {
			continue
		}
		if err := s.objects.Delete(ctx, pending); err != nil {
			return err
		}
	}
	queued := next
	queued.PendingKeys = nil
	queued.State, queued.LeaseUntil, queued.ErrorCode = model.IndexPending, 0, "PREPARATION_RETRY"
	return s.repo.SaveIndexJob(ctx, &next, &queued, nil, false, s.now().UnixMilli())
}

func indexMember(job *model.IndexJob) model.IndexMember {
	return model.IndexMember{Resource: job.Resource, Version: job.Version, RunID: job.RunID, Revision: job.Revision}
}

func (s *IndexingService) inventory(ctx context.Context, key model.IndexResource) ([]string, error) {
	keys, err := s.objects.List(ctx, key.Prefix())
	if err != nil {
		return nil, err
	}
	legacy := legacyIndexKeys(key)
	if len(legacy) > 0 {
		listed, err := s.objects.List(ctx, legacy[0])
		if err != nil {
			return nil, err
		}
		for _, object := range listed {
			if object == legacy[0] || object == legacy[1] {
				keys = append(keys, object)
			}
		}
	}
	return keys, nil
}
func (s *IndexingService) inventoryMatches(ctx context.Context, key model.IndexResource, expected []string) (bool, error) {
	keys, err := s.inventory(ctx, key)
	if err != nil {
		return false, err
	}
	set := map[string]bool{}
	for _, object := range expected {
		set[object] = true
	}
	if len(keys) != len(set) {
		return false, nil
	}
	for _, object := range keys {
		if !set[object] {
			return false, nil
		}
	}
	return true, nil
}
func (s *IndexingService) cleanup(ctx context.Context, key model.IndexResource, keep []string) error {
	keys, err := s.inventory(ctx, key)
	if err != nil {
		return err
	}
	set := map[string]bool{}
	for _, object := range keep {
		set[object] = true
	}
	for _, object := range keys {
		if !set[object] {
			if err := s.objects.Delete(ctx, object); err != nil {
				return err
			}
		}
	}
	complete, err := s.inventoryMatches(ctx, key, keep)
	if err != nil {
		return err
	}
	if !complete {
		return ErrIndexChanged
	}
	return nil
}

func (s *IndexingService) cleanupTracked(ctx context.Context, job *model.IndexJob, keep []string) error {
	inventory, err := s.inventory(ctx, job.Resource)
	if err != nil {
		return err
	}
	retained, removed := map[string]bool{}, map[string]bool{}
	for _, key := range keep {
		retained[key] = true
	}
	// Legacy vectors can outlive their S3 objects and predate this job record.
	// Their exact canonical URI remains known even when inventory is empty.
	for _, list := range [][]string{inventory, job.Keys, job.PendingKeys, job.RemovedKeys, legacyIndexKeys(job.Resource)} {
		for _, key := range list {
			if !retained[key] && !strings.HasSuffix(key, ".metadata.json") {
				removed[key] = true
			}
		}
	}
	next := *job
	next.RemovedKeys = nil
	for key := range removed {
		next.RemovedKeys = append(next.RemovedKeys, key)
	}
	sort.Strings(next.RemovedKeys)
	next.Keys, next.PendingKeys = append([]string(nil), keep...), nil
	// Record deletions before S3 mutation. A crash cannot erase the document
	// identifiers needed to verify removal after a partially failed full sync.
	if err := s.repo.SaveIndexJob(ctx, job, &next, nil, false, s.now().UnixMilli()); err != nil {
		return err
	}
	*job = next
	return s.cleanup(ctx, job.Resource, keep)
}

func (s *IndexingService) finish(ctx context.Context, member model.IndexMember, syncID string, success bool) error {
	job, err := s.repo.GetIndexJob(ctx, member.Resource)
	if err != nil {
		return err
	}
	// Already finalized, superseded by a stream event, or failed before staging.
	if job == nil || job.Version != member.Version || job.RunID != member.RunID || job.State != model.IndexWaitingSync {
		return nil
	}
	next := *job
	next.SyncID, next.UpdatedAt = syncID, s.now().UnixMilli()
	if !success {
		var removed bool
		success, removed, err = s.documentsSucceeded(ctx, job.Keys, job.RemovedKeys)
		if err != nil {
			return err
		}
		if removed {
			next.RemovedKeys = nil
		}
	}
	// Read after the provider status request: that request can take time, and
	// document success is never proof that its source is still current.
	current, err := s.ReadSource(ctx, member.Resource, false)
	if err != nil {
		if permanentIndexSourceError(err) || errors.Is(err, ErrIndexChanged) {
			return s.repo.RequestIndexResource(ctx, member.Resource, "", s.now().UnixMilli())
		}
		return err
	}
	complete, err := s.inventoryMatches(ctx, member.Resource, job.Keys)
	if err != nil {
		return err
	}
	if current.Revision != member.Revision || !complete {
		return s.repo.RequestIndexResource(ctx, member.Resource, "", s.now().UnixMilli())
	}
	if !success {
		s.failJob(&next, "INGESTION_FAILED")
		return s.repo.SaveIndexJob(ctx, job, &next, current.Record, true, s.now().UnixMilli())
	}
	next.State, next.ErrorCode = current.Outcome, current.ErrorCode
	next.RemovedKeys = nil
	next.FailureCount, next.RetryAfter = 0, 0
	err = s.repo.SaveIndexJob(ctx, job, &next, current.Record, true, s.now().UnixMilli())
	if errors.Is(err, repository.ErrConditionFailed) {
		return s.repo.RequestIndexResource(ctx, member.Resource, "", s.now().UnixMilli())
	}
	return err
}

func (s *IndexingService) documentsSucceeded(ctx context.Context, keys, removed []string) (bool, bool, error) {
	documents := []string{}
	expected := map[string]string{}
	for _, key := range keys {
		if !strings.HasSuffix(key, ".metadata.json") {
			documents = append(documents, key)
			expected[key] = "INDEXED"
		}
	}
	for _, key := range removed {
		if _, duplicate := expected[key]; duplicate {
			return false, false, ErrIndexInvalid
		}
		documents = append(documents, key)
		expected[key] = "NOT_FOUND"
	}
	if len(documents) == 0 {
		return true, true, nil
	}
	statuses, err := s.ingestion.Documents(ctx, documents)
	if err != nil {
		return false, false, err
	}
	indexed, deleted := true, true
	for _, key := range documents {
		if statuses[key] == expected[key] {
			continue
		}
		switch statuses[key] {
		case "INDEXED", "FAILED", "PARTIALLY_INDEXED", "METADATA_PARTIALLY_INDEXED", "METADATA_UPDATE_FAILED", "IGNORED", "NOT_FOUND":
			indexed = false
			if expected[key] == "NOT_FOUND" {
				deleted = false
			}
		case "STARTING", "PENDING", "IN_PROGRESS", "DELETING", "DELETE_IN_PROGRESS":
			return false, false, ErrIndexSyncBusy
		default:
			return false, false, ErrIndexInvalid
		}
	}
	return indexed, deleted, nil
}

func (s *IndexingService) failJob(job *model.IndexJob, code string) {
	if job.FailureCount < 0 {
		job.FailureCount = 0
	}
	if job.FailureCount < 6 {
		job.FailureCount++
	}
	delay := time.Minute << min(job.FailureCount-1, 5)
	if delay > 30*time.Minute {
		delay = 30 * time.Minute
	}
	now := s.now()
	job.State, job.ErrorCode, job.LeaseUntil = model.IndexFailed, code, 0
	job.RetryAfter, job.UpdatedAt = now.Add(delay).UnixMilli(), now.UnixMilli()
}

// Status revalidates source bytes as well as the canonical record. A stored
// INDEXED flag alone is not authorization or proof of the current revision.
func (s *IndexingService) Status(ctx context.Context, key model.IndexResource) (*model.IndexJob, error) {
	job, err := s.repo.GetIndexJob(ctx, key)
	if err != nil || job == nil {
		return job, err
	}
	current, err := s.ReadSource(ctx, key, false)
	if err != nil {
		return nil, err
	}
	result := *job
	// A source-read failure can precede publication of any new revision.
	// Preserve that failure when it belongs to the current requested source.
	currentFailure := result.State == model.IndexFailed && result.DesiredRevision == current.Revision
	if result.Revision != current.Revision && !currentFailure {
		result.State = model.IndexPending
	}
	if result.State == model.IndexIndexed || result.State == model.IndexDeleted {
		complete, err := s.inventoryMatches(ctx, key, job.Keys)
		if err != nil {
			return nil, err
		}
		if !complete {
			result.State = model.IndexPending
		}
	}
	return &result, nil
}
