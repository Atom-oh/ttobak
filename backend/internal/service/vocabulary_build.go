package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/transcribe"
	transcribeTypes "github.com/aws/aws-sdk-go-v2/service/transcribe/types"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// Transcribe custom vocabulary builds (A/B slots, vocabulary_build.go).
const (
	DefaultVocabRebuildMinInterval = 10 * time.Minute
	// vocabSubmitSkew tolerates clock and timestamp-precision differences when
	// a poll compares the provider's LastModifiedTime with SubmittedAt.
	vocabSubmitSkew = 5 * time.Second
	// vocabSlotGrace delays deleting a demoted (or emptied) vocabulary for
	// streams and jobs that started with it; deletion waits this long after the
	// flip. The demoted slot stays the next build target and may be rebuilt in
	// place during the window (ADR-008 amendment).
	vocabSlotGrace   = 24 * time.Hour
	vocabObjectDir   = "vocab/"
	vocabNamePrefix  = "ttobak-vocab-"
	vocabEnsureSteps = 8
)

const (
	vocabFailureProvider = "PROVIDER"
	vocabFailureQuota    = "QUOTA"
	vocabQuotaWarning    = "vocabulary quota reached; recognition keeps the previous vocabulary"
	vocabCleanupWarning  = "old vocabulary cleanup is pending"
)

// vocabBuildLanguages are the languages EnsureVocabularies builds. en-US is
// deferred until a consumer exists: batch uses ko-KR only and no response
// carries a name for live streams (ADR-008 amendment). Its table rules stay.
var vocabBuildLanguages = []string{model.VocabLangKo}

// VocabularyObjectAPI is the S3 surface the builder uses for table files.
type VocabularyObjectAPI interface {
	PutObject(ctx context.Context, in *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	DeleteObject(ctx context.Context, in *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

type vocabularyBuilder struct {
	objects     VocabularyObjectAPI
	bucket      string
	minInterval time.Duration
	newName     func() (string, error)
}

// SetVocabularyBuilder enables EnsureVocabularies (API Lambda only).
func (s *DictionaryService) SetVocabularyBuilder(objects VocabularyObjectAPI, bucket string, minInterval time.Duration) {
	if objects == nil || bucket == "" {
		s.builder = nil
		return
	}
	if minInterval < 0 {
		minInterval = 0
	}
	s.builder = &vocabularyBuilder{objects: objects, bucket: bucket, minInterval: minInterval, newName: randomVocabularyName}
}

// ParseVocabRebuildMinInterval parses a Go duration; bad values give 10m.
func ParseVocabRebuildMinInterval(raw string) time.Duration {
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || d < 0 {
		return DefaultVocabRebuildMinInterval
	}
	return d
}

// randomVocabularyName returns ttobak-vocab-{16 hex}, not derivable from the user.
func randomVocabularyName() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return vocabNamePrefix + hex.EncodeToString(b[:]), nil
}

// vocabObjectKey is vocab/{name}.{fingerprint}.tsv: a stale submitter never
// overwrites a different table. It is not attempt-unique (unlike ADR-037), so
// two attempts of the same table share one key.
func vocabObjectKey(name, fingerprint string) string {
	return vocabObjectDir + name + "." + fingerprint + ".tsv"
}

// submittedProviderResult accepts a provider result only when it was modified
// at or after the submit claim, so an older build on a reused slot (or a claim
// whose provider call never ran) is never published as the claimed table.
func submittedProviderResult(modified *time.Time, submittedAt string) bool {
	if modified == nil || submittedAt == "" {
		return false
	}
	t, err := time.Parse(model.DictTimeLayout, submittedAt)
	return err == nil && !modified.Before(t.Add(-vocabSubmitSkew))
}

func elapsedSince(stamp string, now time.Time) time.Duration {
	if stamp == "" {
		return 1<<63 - 1
	}
	t, err := time.Parse(model.DictTimeLayout, stamp)
	if err != nil {
		return 1<<63 - 1
	}
	return now.Sub(t)
}

func laterStamp(a, b string) string {
	if a > b {
		return a
	}
	return b
}

func vocabularyView(st *model.VocabularyState) model.VocabularyView {
	if st == nil {
		return model.VocabularyView{Status: model.VocabStatusNone}
	}
	status := st.Status
	if status == "" {
		status = model.VocabStatusNone
	}
	return model.VocabularyView{Status: status, ActiveReady: st.ActiveReady && st.ActiveName != "", FailureCode: st.FailureCode, TermCount: st.TermCount}
}

// vocabularyViews reads the personal states without writing.
func (s *DictionaryService) vocabularyViews(ctx context.Context, userID string) *model.VocabularyViews {
	view := func(lang string) model.VocabularyView {
		st, err := s.repo.GetVocabularyState(ctx, userID, model.VocabContextPersonal, lang)
		if err != nil {
			log.Printf("failed to read %s vocabulary state for user %s: %v", lang, userID, err)
		}
		return vocabularyView(st)
	}
	return &model.VocabularyViews{Ko: view(model.VocabLangKo), En: view(model.VocabLangEn)}
}

// EnsureVocabularies advances the personal builds (vocabBuildLanguages) by
// bounded steps (retire, submit, poll, flip or claim); manual grants one
// interval bypass. Without SetVocabularyBuilder it only reads. Failures are
// warnings. The dictionary GET runs it, including the one each recording start
// makes, so a finished build flips without a settings-page visit.
func (s *DictionaryService) EnsureVocabularies(ctx context.Context, userID, vocabContext string, manual bool) (*model.VocabularyViews, []string) {
	if vocabContext != model.VocabContextPersonal || s.builder == nil || s.transcribeClient == nil {
		return s.vocabularyViews(ctx, userID), nil
	}
	var warnings []string
	for _, lang := range vocabBuildLanguages {
		warnings = append(warnings, s.ensureVocabulary(ctx, userID, lang, manual)...)
	}
	return s.vocabularyViews(ctx, userID), dedupeWarnings(warnings)
}

func dedupeWarnings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range in {
		if !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}

// vocabStep is one state-machine step's outcome: continue re-reads the row.
type vocabStep struct {
	more     bool
	warnings []string
}

// ensureVocabulary reads the state and only then lists the terms, on every
// step. A term write committed after the listing is followed by its writer's
// own ensure, which either moves the row first (failing this step's CAS) or
// sees this step's write; every step that could reach the provider relists,
// so a deleted term is never built back in from a stale snapshot.
func (s *DictionaryService) ensureVocabulary(ctx context.Context, userID, lang string, manual bool) []string {
	bypass := manual
	var warnings []string
	for step := 0; step < vocabEnsureSteps; step++ {
		st, err := s.repo.GetVocabularyState(ctx, userID, model.VocabContextPersonal, lang)
		if err != nil {
			log.Printf("vocabulary state read failed for user %s: %v", userID, err)
			return append(warnings, vocabularyRebuildWarning)
		}
		rows, err := s.repo.ListDictTerms(ctx, model.PrefixUser+userID)
		if err != nil {
			log.Printf("vocabulary build skipped for user %s: %v", userID, err)
			return append(warnings, vocabularyRebuildWarning)
		}
		plan := planVocabulary(rows, lang)
		if st == nil {
			if plan.empty {
				return warnings
			}
			err := s.repo.CreateMigratedVocabularyState(ctx, userID, &model.VocabularyState{
				PK: model.PrefixUser + userID, SK: model.VocabSK(model.VocabContextPersonal, lang),
				Context: model.VocabContextPersonal, Language: lang, Status: model.VocabStatusNone,
				UpdatedAt: s.stamp(), Version: 1, EntityType: model.EntityTypeVocabularyState,
			})
			switch {
			case errors.Is(err, repository.ErrLegacyBlobChanged):
				return warnings // migration owns the first row; build after it
			case err != nil && !errors.Is(err, repository.ErrConditionFailed):
				log.Printf("vocabulary state create failed for user %s: %v", userID, err)
				return append(warnings, vocabularyRebuildWarning)
			}
			continue
		}
		out := s.vocabularyStep(ctx, userID, st, plan, &bypass)
		warnings = append(warnings, out.warnings...)
		if !out.more {
			return warnings
		}
	}
	return warnings
}

// casStep applies a version-guarded change; a lost race re-reads.
func (s *DictionaryService) casStep(ctx context.Context, userID string, st *model.VocabularyState, change repository.VocabularyStateChange) vocabStep {
	step, _ := s.casApplied(ctx, userID, st, change)
	return step
}

// casApplied is casStep that also reports whether this caller's change won.
func (s *DictionaryService) casApplied(ctx context.Context, userID string, st *model.VocabularyState, change repository.VocabularyStateChange) (vocabStep, bool) {
	err := s.repo.UpdateVocabularyState(ctx, userID, st.Context, st.Language, st.Version, change, s.stamp())
	switch {
	case err == nil:
		return vocabStep{more: true}, true
	case errors.Is(err, repository.ErrConditionFailed):
		return vocabStep{more: true}, false
	}
	log.Printf("vocabulary state update failed for user %s: %v", userID, err)
	return vocabStep{warnings: []string{vocabularyRebuildWarning}}, false
}

func strp(s string) *string { return &s }
func boolp(b bool) *bool    { return &b }
func intp(n int) *int       { return &n }

func (s *DictionaryService) vocabularyStep(ctx context.Context, userID string, st *model.VocabularyState, plan vocabPlan, bypass *bool) vocabStep {
	now := s.now()
	// 1. A retired slot is deleted before anything else. The demoted legacy
	// name waits out the grace window there, so it never blocks a build.
	legacyWaiting := st.RetiredName == legacyVocabularyName(userID) && elapsedSince(st.FlippedAt, now) < vocabSlotGrace
	if st.RetiredName != "" && !legacyWaiting {
		if err := s.deleteVocabulary(ctx, st.RetiredName); err != nil {
			log.Printf("vocabulary %s deletion failed: %v", st.RetiredName, err)
			return vocabStep{warnings: []string{vocabCleanupWarning}}
		}
		return s.casStep(ctx, userID, st, repository.VocabularyStateChange{RetiredName: strp("")})
	}
	// 2. A legacy in-place build (no fingerprint) only refreshes ActiveReady.
	if st.Status == model.VocabStatusBuilding && st.BuildingFingerprint == "" {
		if synced := s.syncVocabularyState(ctx, userID, st); synced != st { // synced or raced: re-read
			return vocabStep{more: true}
		}
	}
	// An emptied language stops advertising at once, before any claimed build
	// is advanced: the claim (submitted or not) is dropped, so a pending
	// result is never flipped or served (ADR-008 amendment).
	if plan.empty {
		return s.emptyVocabularyStep(ctx, userID, st, now)
	}
	// A claimed build is resumed or polled whatever its age: the provider
	// state decides, so an aged build is never reclaimed or deleted unpolled.
	if st.Status == model.VocabStatusBuilding && st.BuildingFingerprint != "" && st.InactiveName != "" {
		return s.advanceBuild(ctx, userID, st, plan, bypass)
	}
	if st.Status == model.VocabStatusBuilding && st.BuildingFingerprint == "" && st.InactiveName == "" && st.ActiveName != "" {
		// The legacy build is still pending: an A/B claim now would stop step 2
		// from ever marking it ready, hiding a READY result from batch.
		return vocabStep{}
	}
	// 3. Retire the demoted (or failed) slot after grace; never a claimed build.
	if st.InactiveName != "" && st.RetiredName == "" && (st.InactiveName == legacyVocabularyName(userID) || elapsedSince(st.FlippedAt, now) >= vocabSlotGrace) {
		return s.casStep(ctx, userID, st, repository.VocabularyStateChange{RetiredName: &st.InactiveName, InactiveName: strp("")})
	}
	if st.ActiveReady && st.ActiveName != "" && plan.fingerprint == st.ActiveFingerprint {
		if st.Status != model.VocabStatusReady || st.FailureCode != "" || st.TermCount != plan.count {
			return s.casStep(ctx, userID, st, repository.VocabularyStateChange{Status: strp(model.VocabStatusReady), FailureCode: strp(""), TermCount: intp(plan.count)})
		}
		return vocabStep{}
	}
	if st.Status == model.VocabStatusFailed && st.BuildingFingerprint == plan.fingerprint && !*bypass {
		return vocabStep{} // the same table failed; wait for a change or a manual write
	}
	useBypass := *bypass || st.BypassPending
	if !useBypass && elapsedSince(laterStamp(st.FlippedAt, st.BuildStartedAt), now) < s.builder.minInterval {
		return vocabStep{}
	}
	target := st.InactiveName
	if target == legacyVocabularyName(userID) {
		return vocabStep{} // unreachable: step 3 retires it; never rebuilt
	}
	if target == "" {
		name, err := s.builder.newName()
		if err != nil {
			log.Printf("vocabulary name generation failed: %v", err)
			return vocabStep{warnings: []string{vocabularyRebuildWarning}}
		}
		target = name
	}
	out, won := s.casApplied(ctx, userID, st, repository.VocabularyStateChange{
		Status: strp(model.VocabStatusBuilding), BuildingFingerprint: strp(plan.fingerprint),
		BuildStartedAt: strp(now.Format(model.DictTimeLayout)), InactiveName: &target,
		BuildSubmitted: boolp(false), BypassPending: boolp(false), FailureCode: strp(""),
		TermCount: intp(plan.count), EmptySince: strp(""),
	})
	if won {
		*bypass = false // a lost claim race keeps the manual bypass
	}
	return out
}

// advanceBuild submits or polls a claimed build; plan is never empty here.
func (s *DictionaryService) advanceBuild(ctx context.Context, userID string, st *model.VocabularyState, plan vocabPlan, bypass *bool) vocabStep {
	if !st.BuildSubmitted {
		if plan.fingerprint != st.BuildingFingerprint {
			return s.casStep(ctx, userID, st, repository.VocabularyStateChange{BuildingFingerprint: strp(plan.fingerprint), TermCount: intp(plan.count)})
		}
		return s.submitBuild(ctx, userID, st, plan)
	}
	if *bypass && plan.fingerprint != st.BuildingFingerprint && !st.BypassPending {
		out, won := s.casApplied(ctx, userID, st, repository.VocabularyStateChange{BypassPending: boolp(true)})
		if won {
			*bypass = false
		}
		return out
	}
	out, err := s.transcribeClient.GetVocabulary(ctx, &transcribe.GetVocabularyInput{VocabularyName: aws.String(st.InactiveName)})
	if err != nil {
		if vocabularyMissing(err) {
			return s.casStep(ctx, userID, st, repository.VocabularyStateChange{BuildSubmitted: boolp(false)})
		}
		log.Printf("vocabulary %s poll failed: %v", st.InactiveName, err)
		return vocabStep{warnings: []string{vocabularyRebuildWarning}}
	}
	if out.VocabularyState != transcribeTypes.VocabularyStatePending && !submittedProviderResult(out.LastModifiedTime, st.SubmittedAt) {
		// The slot shows an older build: the submission never reached the
		// provider (a crash after the claim, or a released claim). Resubmit.
		return s.casStep(ctx, userID, st, repository.VocabularyStateChange{BuildSubmitted: boolp(false), ExpectBuildingFingerprint: &st.BuildingFingerprint})
	}
	switch out.VocabularyState {
	case transcribeTypes.VocabularyStateReady:
		change := repository.VocabularyStateChange{ExpectBuildingFingerprint: &st.BuildingFingerprint,
			ActiveName: &st.InactiveName, InactiveName: &st.ActiveName, ActiveFingerprint: &st.BuildingFingerprint,
			ActiveReady: boolp(true), Status: strp(model.VocabStatusReady), FailureCode: strp(""),
			FlippedAt: strp(s.stamp()), BuildSubmitted: boolp(false), EmptySince: strp(""),
		}
		if st.ActiveName == legacyVocabularyName(userID) && st.RetiredName == "" {
			// The legacy name is retired at its first flip; step 1 deletes it
			// after the grace window, and the next build gets a fresh name.
			change.InactiveName, change.RetiredName = strp(""), &st.ActiveName
		}
		step := s.casStep(ctx, userID, st, change)
		if step.more {
			step.warnings = s.deleteVocabularyFile(ctx, st.InactiveName, st.BuildingFingerprint)
		}
		return step
	case transcribeTypes.VocabularyStateFailed:
		step := s.casStep(ctx, userID, st, repository.VocabularyStateChange{ExpectBuildingFingerprint: &st.BuildingFingerprint,
			Status: strp(model.VocabStatusFailed), FailureCode: strp(vocabFailureProvider), BuildSubmitted: boolp(false),
		})
		step.warnings = append(s.deleteVocabularyFile(ctx, st.InactiveName, st.BuildingFingerprint), vocabularyRebuildWarning)
		step.more = false
		return step
	}
	return vocabStep{} // still PENDING
}

// submitBuild writes the immutable table file, records the submit claim
// (version, fingerprint and BuildSubmitted guarded) and only then creates or
// updates the inactive slot, so a stale caller never reaches the provider.
func (s *DictionaryService) submitBuild(ctx context.Context, userID string, st *model.VocabularyState, plan vocabPlan) vocabStep {
	name, key := st.InactiveName, vocabObjectKey(st.InactiveName, plan.fingerprint)
	if _, err := s.builder.objects.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.builder.bucket), Key: aws.String(key), Body: strings.NewReader(plan.body),
		ContentType: aws.String("text/tab-separated-values; charset=utf-8"),
	}); err != nil {
		log.Printf("vocabulary file write failed for %s: %v", name, err)
		return vocabStep{warnings: []string{vocabularyRebuildWarning}}
	}
	submittedAt := s.stamp()
	claim := repository.VocabularyStateChange{BuildSubmitted: boolp(true), SubmittedAt: &submittedAt, ExpectBuildingFingerprint: &plan.fingerprint}
	if err := s.repo.UpdateVocabularyState(ctx, userID, st.Context, st.Language, st.Version, claim, submittedAt); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			// Another caller moved the row; its table has its own key. A
			// leftover file here expires by the vocab/ lifecycle rule.
			return vocabStep{more: true}
		}
		log.Printf("vocabulary submit claim failed for user %s: %v", userID, err)
		return vocabStep{warnings: []string{vocabularyRebuildWarning}}
	}
	// Another caller may still move the row during the provider call (an
	// emptied plan, or a stall across a whole build). Every later CAS is
	// version and fingerprint guarded, so a release below then fails harmlessly
	// and only a row still holding this claim ever polls the result.
	claimed := *st
	claimed.Version++
	claimed.BuildSubmitted, claimed.SubmittedAt = true, submittedAt
	uri := fmt.Sprintf("s3://%s/%s", s.builder.bucket, key)
	lang := transcribeTypes.LanguageCode(st.Language)
	var err error
	if _, getErr := s.transcribeClient.GetVocabulary(ctx, &transcribe.GetVocabularyInput{VocabularyName: aws.String(name)}); getErr == nil {
		_, err = s.transcribeClient.UpdateVocabulary(ctx, &transcribe.UpdateVocabularyInput{VocabularyName: aws.String(name), LanguageCode: lang, VocabularyFileUri: aws.String(uri)})
	} else if vocabularyMissing(getErr) {
		_, err = s.transcribeClient.CreateVocabulary(ctx, &transcribe.CreateVocabularyInput{VocabularyName: aws.String(name), LanguageCode: lang, VocabularyFileUri: aws.String(uri)})
	} else {
		err = getErr
	}
	if err == nil {
		return vocabStep{} // poll on a later call
	}
	release := func(change repository.VocabularyStateChange, extra ...string) vocabStep {
		change.BuildSubmitted, change.ExpectBuildingFingerprint = boolp(false), &plan.fingerprint
		step := s.casStep(ctx, userID, &claimed, change)
		return vocabStep{warnings: append(step.warnings, extra...)}
	}
	switch classifyVocabularyError(err) {
	case vocabErrConflict:
		return release(repository.VocabularyStateChange{}) // keep the claim; a later call resubmits
	case vocabErrLimit:
		return release(repository.VocabularyStateChange{Status: strp(model.VocabStatusQuotaExceeded), FailureCode: strp(vocabFailureQuota)},
			append(s.deleteVocabularyFile(ctx, name, plan.fingerprint), vocabQuotaWarning)...)
	case vocabErrBadRequest:
		return release(repository.VocabularyStateChange{Status: strp(model.VocabStatusFailed), FailureCode: strp(vocabFailureProvider)},
			append(s.deleteVocabularyFile(ctx, name, plan.fingerprint), vocabularyRebuildWarning)...)
	}
	// Ambiguous: the provider may have accepted it. Keep the claim; the poll
	// accepts only a result modified after SubmittedAt and otherwise resubmits.
	log.Printf("vocabulary %s submit failed: %v", name, err)
	return vocabStep{warnings: []string{vocabularyRebuildWarning}}
}

// emptyVocabularyStep handles a language without eligible terms: stop
// advertising at once (also dropping any claimed or submitted build, whose
// provider result is then never polled or flipped), delete both slots after
// the grace window.
func (s *DictionaryService) emptyVocabularyStep(ctx context.Context, userID string, st *model.VocabularyState, now time.Time) vocabStep {
	if st.ActiveName == "" && st.InactiveName == "" {
		if st.Status != model.VocabStatusNone || st.ActiveReady || st.FailureCode != "" || st.EmptySince != "" {
			return s.casStep(ctx, userID, st, repository.VocabularyStateChange{
				Status: strp(model.VocabStatusNone), ActiveReady: boolp(false), FailureCode: strp(""),
				ActiveFingerprint: strp(""), BuildingFingerprint: strp(""), EmptySince: strp(""), TermCount: intp(0),
			})
		}
		return vocabStep{}
	}
	if st.EmptySince == "" || st.ActiveReady || st.Status != model.VocabStatusNone || st.BuildSubmitted || st.BuildingFingerprint != "" {
		since := st.EmptySince
		if since == "" {
			since = now.Format(model.DictTimeLayout)
		}
		return s.casStep(ctx, userID, st, repository.VocabularyStateChange{
			Status: strp(model.VocabStatusNone), ActiveReady: boolp(false), FailureCode: strp(""),
			ActiveFingerprint: strp(""), BuildingFingerprint: strp(""), EmptySince: &since, TermCount: intp(0),
			BuildSubmitted: boolp(false), BypassPending: boolp(false),
		})
	}
	if elapsedSince(st.EmptySince, now) < vocabSlotGrace || st.RetiredName != "" {
		return vocabStep{} // a waiting legacy retirement keeps the single retired slot
	}
	if st.InactiveName != "" {
		return s.casStep(ctx, userID, st, repository.VocabularyStateChange{RetiredName: &st.InactiveName, InactiveName: strp("")})
	}
	return s.casStep(ctx, userID, st, repository.VocabularyStateChange{RetiredName: &st.ActiveName, ActiveName: strp("")})
}

// deleteVocabulary deletes a slot; an already missing vocabulary is success.
func (s *DictionaryService) deleteVocabulary(ctx context.Context, name string) error {
	_, err := s.transcribeClient.DeleteVocabulary(ctx, &transcribe.DeleteVocabularyInput{VocabularyName: aws.String(name)})
	if err == nil || classifyVocabularyError(err) == vocabErrNotFound {
		return nil
	}
	if _, getErr := s.transcribeClient.GetVocabulary(ctx, &transcribe.GetVocabularyInput{VocabularyName: aws.String(name)}); getErr != nil && vocabularyMissing(getErr) {
		return nil
	}
	return err
}

// deleteVocabularyFile removes one table file; lifecycle covers a failure.
func (s *DictionaryService) deleteVocabularyFile(ctx context.Context, name, fingerprint string) []string {
	if _, err := s.builder.objects.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.builder.bucket), Key: aws.String(vocabObjectKey(name, fingerprint))}); err != nil {
		log.Printf("vocabulary file cleanup failed for %s: %v", name, err)
		return []string{"vocabulary file cleanup incomplete"}
	}
	return nil
}

// verifiedReadyBuild reports, read-only, whether a submitted A/B build is
// READY at the provider with a result no older than its submit claim. Batch
// uses it so a finished build serves before any API call flips it.
func (s *DictionaryService) verifiedReadyBuild(ctx context.Context, st *model.VocabularyState) bool {
	if st.Status != model.VocabStatusBuilding || !st.BuildSubmitted || st.InactiveName == "" || st.BuildingFingerprint == "" || s.transcribeClient == nil {
		return false
	}
	out, err := s.transcribeClient.GetVocabulary(ctx, &transcribe.GetVocabularyInput{VocabularyName: aws.String(st.InactiveName)})
	if err != nil {
		log.Printf("failed to get vocabulary status for %s: %v", st.InactiveName, err)
		return false
	}
	return out.VocabularyState == transcribeTypes.VocabularyStateReady && submittedProviderResult(out.LastModifiedTime, st.SubmittedAt)
}
