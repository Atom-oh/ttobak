package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/transcribe"
	transcribeTypes "github.com/aws/aws-sdk-go-v2/service/transcribe/types"
	smithy "github.com/aws/smithy-go"
	"golang.org/x/text/unicode/norm"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// Transcribe custom vocabulary builds (A/B slots). The table file has four
// columns; SoundsLike and IPA stay empty.
const (
	VocabTSVHeader = "Phrase\tSoundsLike\tIPA\tDisplayAs\n"
	VocabMaxRows   = 1500
	VocabMaxBytes  = 48 << 10
	// VocabEnMinTerms is the en-US build threshold; fewer terms build nothing.
	VocabEnMinTerms = 5

	DefaultVocabRebuildMinInterval = 10 * time.Minute
	vocabBuildStaleAfter           = 30 * time.Minute
	// vocabSlotGrace keeps a demoted (or emptied) vocabulary for streams that
	// started with it; deletion waits this long after the flip.
	vocabSlotGrace   = 24 * time.Hour
	vocabObjectDir   = "vocab/"
	vocabNamePrefix  = "ttobak-vocab-"
	vocabEnsureSteps = 8
	vocabDisplayMax  = 256

	// koDottedAcronymsAccepted stays false until rollout acceptance records
	// that ko-KR accepts dotted acronyms (E.K.S.); until then such ko phrases
	// are dropped so one row cannot fail the whole build.
	koDottedAcronymsAccepted = false
)

const (
	vocabFailureProvider = "PROVIDER"
	vocabFailureQuota    = "QUOTA"
	vocabQuotaWarning    = "vocabulary quota reached; recognition keeps the previous vocabulary"
	vocabCleanupWarning  = "old vocabulary cleanup is pending"
)

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

func vocabObjectKey(name string) string { return vocabObjectDir + name + ".tsv" }

func isASCIILetter(r rune) bool { return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') }

// TranscribePhrase is the minimal phrase derivation: NFKC, trimmed, and
// space runs joined with "-" (Transcribe phrases cannot contain spaces).
func TranscribePhrase(raw string) string {
	return strings.Join(strings.Fields(norm.NFKC.String(raw)), "-")
}

// ValidTranscribePhrase applies the table rules: ko-KR [A-Za-z-.] plus Hangul
// syllables, en-US [A-Za-z'-.]; at most 256; no leading . ' -, no trailing
// ' -, no "--" or ".."; periods only after a single letter (E.K.S.).
func ValidTranscribePhrase(phrase, lang string) bool {
	runes := []rune(phrase)
	if len(runes) == 0 || len(runes) > TermPhraseMaxRunes {
		return false
	}
	for i, r := range runes {
		switch {
		case isASCIILetter(r), r == '-', r == '.':
		case r == '\'' && lang == model.VocabLangEn:
		case r >= 0xAC00 && r <= 0xD7A3 && lang == model.VocabLangKo:
		default:
			return false
		}
		if i > 0 && (r == '-' || r == '.') && runes[i-1] == r {
			return false
		}
		if r == '.' {
			if i == 0 || !isASCIILetter(runes[i-1]) || (i >= 2 && runes[i-2] != '-' && runes[i-2] != '.') {
				return false
			}
			if lang == model.VocabLangKo && !koDottedAcronymsAccepted {
				return false
			}
		}
	}
	first, last := runes[0], runes[len(runes)-1]
	return first != '.' && first != '\'' && first != '-' && last != '\'' && last != '-'
}

// validVocabDisplay accepts an optional DisplayAs without control, format or
// line-separator characters (a tab would break the table).
func validVocabDisplay(s string) bool {
	return checkTermRunes("displayAs", s) == nil && utf8.RuneCountInString(s) <= vocabDisplayMax
}

type vocabEntry struct{ Phrase, DisplayAs string }

// vocabPlan is the desired table for one language.
type vocabPlan struct {
	body        string
	count       int
	fingerprint string
	empty       bool
}

// vocabularyEntries orders eligible active terms (manual, hits, oldest),
// dedupes by Phrase (first DisplayAs wins) and drops invalid rows singly.
func vocabularyEntries(rows []model.DictTerm, lang string) []vocabEntry {
	eligible := make([]model.DictTerm, 0, len(rows))
	for _, row := range rows {
		if row.State != model.TermStateActive || row.Phrase == "" {
			continue
		}
		if row.TranscribeKo || (lang == model.VocabLangEn && row.TranscribeEn) {
			eligible = append(eligible, row)
		}
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		a, b := eligible[i], eligible[j]
		if (a.Origin == model.TermOriginManual) != (b.Origin == model.TermOriginManual) {
			return a.Origin == model.TermOriginManual
		}
		if a.Hits != b.Hits {
			return a.Hits > b.Hits
		}
		if a.AddedAt != b.AddedAt {
			return a.AddedAt < b.AddedAt
		}
		return a.SK < b.SK
	})
	seen := map[string]bool{}
	var out []vocabEntry
	for _, row := range eligible {
		phrase := TranscribePhrase(row.Phrase)
		display := strings.TrimSpace(row.DisplayAs)
		key := strings.ToLower(phrase)
		if seen[key] || !ValidTranscribePhrase(phrase, lang) || !validVocabDisplay(display) {
			continue
		}
		seen[key] = true
		out = append(out, vocabEntry{Phrase: phrase, DisplayAs: display})
	}
	return out
}

// RenderVocabularyTSV writes the four-column table, stopping at
// VocabMaxRows rows or VocabMaxBytes bytes. It returns the rows written.
func RenderVocabularyTSV(entries []vocabEntry) (string, int) {
	var b strings.Builder
	b.WriteString(VocabTSVHeader)
	n := 0
	for _, e := range entries {
		line := e.Phrase + "\t\t\t" + e.DisplayAs + "\n"
		if n == VocabMaxRows || b.Len()+len(line) > VocabMaxBytes {
			break
		}
		b.WriteString(line)
		n++
	}
	return b.String(), n
}

func planVocabulary(rows []model.DictTerm, lang string) vocabPlan {
	body, n := RenderVocabularyTSV(vocabularyEntries(rows, lang))
	min := 1
	if lang == model.VocabLangEn {
		min = VocabEnMinTerms
	}
	if n < min {
		return vocabPlan{empty: true}
	}
	sum := sha256.Sum256([]byte(body))
	return vocabPlan{body: body, count: n, fingerprint: hex.EncodeToString(sum[:16])}
}

type vocabErrKind int

const (
	vocabErrOther vocabErrKind = iota
	vocabErrNotFound
	vocabErrBadRequest
	vocabErrConflict
	vocabErrLimit
)

// classifyVocabularyError maps Transcribe errors by error code.
func classifyVocabularyError(err error) vocabErrKind {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return vocabErrOther
	}
	return map[string]vocabErrKind{"NotFoundException": vocabErrNotFound, "BadRequestException": vocabErrBadRequest,
		"ConflictException": vocabErrConflict, "LimitExceededException": vocabErrLimit}[apiErr.ErrorCode()]
}

// vocabularyMissing: NotFound, or the BadRequest returned for an unknown name
// (a wrong guess makes CreateVocabulary fail safely with ConflictException).
func vocabularyMissing(err error) bool {
	kind := classifyVocabularyError(err)
	return kind == vocabErrNotFound || kind == vocabErrBadRequest
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

// EnsureVocabularies advances the personal ko-KR and en-US builds by bounded
// steps (retire, submit, poll, flip or claim); manual grants one interval
// bypass. Without SetVocabularyBuilder it only reads. Failures are warnings.
func (s *DictionaryService) EnsureVocabularies(ctx context.Context, userID, vocabContext string, manual bool) (*model.VocabularyViews, []string) {
	if vocabContext != model.VocabContextPersonal || s.builder == nil || s.transcribeClient == nil {
		return s.vocabularyViews(ctx, userID), nil
	}
	rows, err := s.repo.ListDictTerms(ctx, model.PrefixUser+userID)
	if err != nil {
		log.Printf("vocabulary build skipped for user %s: %v", userID, err)
		return s.vocabularyViews(ctx, userID), []string{vocabularyRebuildWarning}
	}
	var warnings []string
	for _, lang := range []string{model.VocabLangKo, model.VocabLangEn} {
		warnings = append(warnings, s.ensureVocabulary(ctx, userID, lang, planVocabulary(rows, lang), manual)...)
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

func (s *DictionaryService) ensureVocabulary(ctx context.Context, userID, lang string, plan vocabPlan, manual bool) []string {
	bypass := manual
	var warnings []string
	for step := 0; step < vocabEnsureSteps; step++ {
		st, err := s.repo.GetVocabularyState(ctx, userID, model.VocabContextPersonal, lang)
		if err != nil {
			log.Printf("vocabulary state read failed for user %s: %v", userID, err)
			return append(warnings, vocabularyRebuildWarning)
		}
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
	err := s.repo.UpdateVocabularyState(ctx, userID, st.Context, st.Language, st.Version, change, s.stamp())
	if err == nil || errors.Is(err, repository.ErrConditionFailed) {
		return vocabStep{more: true}
	}
	log.Printf("vocabulary state update failed for user %s: %v", userID, err)
	return vocabStep{warnings: []string{vocabularyRebuildWarning}}
}

func strp(s string) *string { return &s }
func boolp(b bool) *bool    { return &b }
func intp(n int) *int       { return &n }

func (s *DictionaryService) vocabularyStep(ctx context.Context, userID string, st *model.VocabularyState, plan vocabPlan, bypass *bool) vocabStep {
	now := s.now()
	// 1. A retired slot is deleted before anything else.
	if st.RetiredName != "" {
		if err := s.deleteVocabulary(ctx, st.RetiredName); err != nil {
			log.Printf("vocabulary %s deletion failed: %v", st.RetiredName, err)
			return vocabStep{warnings: []string{vocabCleanupWarning}}
		}
		return s.casStep(ctx, userID, st, repository.VocabularyStateChange{RetiredName: strp("")})
	}
	// 2. A legacy in-place build (no fingerprint) only refreshes ActiveReady;
	// A/B builds never touch ActiveName, so they may proceed meanwhile.
	if st.Status == model.VocabStatusBuilding && st.BuildingFingerprint == "" {
		if synced := s.syncVocabularyState(ctx, userID, st); synced.Version != st.Version {
			return vocabStep{more: true}
		}
	}
	building := st.Status == model.VocabStatusBuilding && st.BuildingFingerprint != "" && st.InactiveName != ""
	if building && elapsedSince(st.BuildStartedAt, now) < vocabBuildStaleAfter {
		return s.advanceBuild(ctx, userID, st, plan, bypass)
	}
	if plan.empty {
		return s.emptyVocabularyStep(ctx, userID, st, now)
	}
	// 3. Steady state is one vocabulary: retire the old slot after grace.
	if st.InactiveName != "" && elapsedSince(st.FlippedAt, now) >= vocabSlotGrace {
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
	switch {
	case target == legacyVocabularyName(userID):
		return vocabStep{} // the derived legacy name is retired, never rebuilt
	case target == "":
		name, err := s.builder.newName()
		if err != nil {
			log.Printf("vocabulary name generation failed: %v", err)
			return vocabStep{warnings: []string{vocabularyRebuildWarning}}
		}
		target = name
	}
	out := s.casStep(ctx, userID, st, repository.VocabularyStateChange{
		Status: strp(model.VocabStatusBuilding), BuildingFingerprint: strp(plan.fingerprint),
		BuildStartedAt: strp(now.Format(model.DictTimeLayout)), InactiveName: &target,
		BuildSubmitted: boolp(false), BypassPending: boolp(false), FailureCode: strp(""),
		TermCount: intp(plan.count), EmptySince: strp(""),
	})
	if out.more {
		*bypass = false
	}
	return out
}

// advanceBuild submits or polls a claimed build that is not stale.
func (s *DictionaryService) advanceBuild(ctx context.Context, userID string, st *model.VocabularyState, plan vocabPlan, bypass *bool) vocabStep {
	if !st.BuildSubmitted {
		if plan.empty {
			// Nothing left to build: release the claim and keep the active slot.
			status := model.VocabStatusNone
			if st.ActiveReady {
				status = model.VocabStatusReady
			}
			return s.casStep(ctx, userID, st, repository.VocabularyStateChange{Status: &status, BuildingFingerprint: strp("")})
		}
		if plan.fingerprint != st.BuildingFingerprint {
			return s.casStep(ctx, userID, st, repository.VocabularyStateChange{BuildingFingerprint: strp(plan.fingerprint), TermCount: intp(plan.count)})
		}
		return s.submitBuild(ctx, userID, st, plan)
	}
	if *bypass && !plan.empty && plan.fingerprint != st.BuildingFingerprint && !st.BypassPending {
		*bypass = false
		return s.casStep(ctx, userID, st, repository.VocabularyStateChange{BypassPending: boolp(true)})
	}
	out, err := s.transcribeClient.GetVocabulary(ctx, &transcribe.GetVocabularyInput{VocabularyName: aws.String(st.InactiveName)})
	if err != nil {
		if vocabularyMissing(err) {
			return s.casStep(ctx, userID, st, repository.VocabularyStateChange{BuildSubmitted: boolp(false)})
		}
		log.Printf("vocabulary %s poll failed: %v", st.InactiveName, err)
		return vocabStep{}
	}
	switch out.VocabularyState {
	case transcribeTypes.VocabularyStateReady:
		step := s.casStep(ctx, userID, st, repository.VocabularyStateChange{
			ActiveName: &st.InactiveName, InactiveName: &st.ActiveName, ActiveFingerprint: &st.BuildingFingerprint,
			ActiveReady: boolp(true), Status: strp(model.VocabStatusReady), FailureCode: strp(""),
			FlippedAt: strp(s.stamp()), BuildSubmitted: boolp(false), EmptySince: strp(""),
		})
		if step.more {
			step.warnings = s.deleteVocabularyFile(ctx, st.InactiveName)
		}
		return step
	case transcribeTypes.VocabularyStateFailed:
		step := s.casStep(ctx, userID, st, repository.VocabularyStateChange{
			Status: strp(model.VocabStatusFailed), FailureCode: strp(vocabFailureProvider), BuildSubmitted: boolp(false),
		})
		step.warnings = append(s.deleteVocabularyFile(ctx, st.InactiveName), vocabularyRebuildWarning)
		step.more = false
		return step
	}
	return vocabStep{} // still PENDING
}

// submitBuild writes the table file and creates or updates the inactive slot.
func (s *DictionaryService) submitBuild(ctx context.Context, userID string, st *model.VocabularyState, plan vocabPlan) vocabStep {
	name, key := st.InactiveName, vocabObjectKey(st.InactiveName)
	if _, err := s.builder.objects.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.builder.bucket), Key: aws.String(key), Body: strings.NewReader(plan.body),
		ContentType: aws.String("text/tab-separated-values; charset=utf-8"),
	}); err != nil {
		log.Printf("vocabulary file write failed for %s: %v", name, err)
		return vocabStep{warnings: []string{vocabularyRebuildWarning}}
	}
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
		step := s.casStep(ctx, userID, st, repository.VocabularyStateChange{BuildSubmitted: boolp(true)})
		step.more = false // poll on a later call
		return step
	}
	switch classifyVocabularyError(err) {
	case vocabErrConflict:
		return vocabStep{} // keep the claim; a later call retries
	case vocabErrLimit:
		step := s.casStep(ctx, userID, st, repository.VocabularyStateChange{Status: strp(model.VocabStatusQuotaExceeded), FailureCode: strp(vocabFailureQuota)})
		return vocabStep{warnings: append(append(step.warnings, s.deleteVocabularyFile(ctx, name)...), vocabQuotaWarning)}
	case vocabErrBadRequest:
		step := s.casStep(ctx, userID, st, repository.VocabularyStateChange{Status: strp(model.VocabStatusFailed), FailureCode: strp(vocabFailureProvider)})
		return vocabStep{warnings: append(append(step.warnings, s.deleteVocabularyFile(ctx, name)...), vocabularyRebuildWarning)}
	}
	log.Printf("vocabulary %s submit failed: %v", name, err)
	return vocabStep{warnings: []string{vocabularyRebuildWarning}}
}

// emptyVocabularyStep handles a language without eligible terms: stop
// advertising at once, delete both slots after the grace window.
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
	if st.EmptySince == "" || st.ActiveReady || st.Status != model.VocabStatusNone {
		since := st.EmptySince
		if since == "" {
			since = now.Format(model.DictTimeLayout)
		}
		return s.casStep(ctx, userID, st, repository.VocabularyStateChange{
			Status: strp(model.VocabStatusNone), ActiveReady: boolp(false), FailureCode: strp(""),
			ActiveFingerprint: strp(""), BuildingFingerprint: strp(""), EmptySince: &since, TermCount: intp(0),
		})
	}
	if elapsedSince(st.EmptySince, now) < vocabSlotGrace {
		return vocabStep{}
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

// deleteVocabularyFile removes the table file; lifecycle covers a failure.
func (s *DictionaryService) deleteVocabularyFile(ctx context.Context, name string) []string {
	if _, err := s.builder.objects.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.builder.bucket), Key: aws.String(vocabObjectKey(name))}); err != nil {
		log.Printf("vocabulary file cleanup failed for %s: %v", name, err)
		return []string{"vocabulary file cleanup incomplete"}
	}
	return nil
}
