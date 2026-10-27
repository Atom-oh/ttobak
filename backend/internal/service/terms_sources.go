package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// Term extraction from KB documents and deep-research reports (layered
// dictionary 4.2). An S3 Object Created event on the KB bucket reaches
// RequestSourceFromObject, which maps it to ttobak.analysis/
// TermsSourceRequested; ProcessSource runs the meeting path's extraction
// prompt and filters on the document text in its own invocation and merges
// into each Account the owner is a member of, else the owner's Personal
// layer. Common is never written from these sources (R6). Logs carry kinds,
// research and Account IDs, a digest of KB keys, codes and counts only.
const (
	SourceTermsModeOff = "off"
	SourceTermsModeOn  = "on"

	// termsSourceMaxBytes caps the text one source sends, as the meeting path
	// caps owner documents: 64 KiB in documents of at most 16 KiB.
	termsSourceMaxBytes = termsDocumentsMaxBytes
	// termsSourceMaxKeyBytes keeps TERMS_SOURCE#kb#{key} within the
	// DynamoDB sort-key limit.
	termsSourceMaxKeyBytes = 900

	termsSourceKBPrefix       = "kb/"
	termsSourceResearchPrefix = "shared/research/"
	kbAccountMetaKey          = "account-id"

	// Source outcomes (codes, safe to log).
	TermsSourceQueued        = "QUEUED"
	TermsSourceSkipped       = "SKIPPED"
	TermsSourceUnsupported   = "UNSUPPORTED"
	TermsSourceNotFound      = "NOT_FOUND"
	TermsSourceNotReady      = "NOT_READY"
	TermsSourceUnchanged     = "UNCHANGED"
	TermsSourceInvalid       = "INVALID"
	TermsSourceNotConfigured = "NOT_CONFIGURED"
)

// errTermsSourceNotReady asks for redelivery: save_report writes the report
// object before it marks the research done.
var errTermsSourceNotReady = errors.New("terms source is not ready")

func sourceTermsMode(env string) string {
	if strings.TrimSpace(os.Getenv(env)) == SourceTermsModeOn {
		return SourceTermsModeOn
	}
	return SourceTermsModeOff
}

// SourceEnabled reports whether a KB or research source of ownerID is
// extracted: its mode is on and the AUTO_TERMS_USERS canary list is empty or
// contains the owner. Other kinds are never sources here.
func (c TermsConfig) SourceEnabled(kind, ownerID string) bool {
	var mode string
	switch kind {
	case model.DictSourceKB:
		mode = c.KBMode
	case model.DictSourceResearch:
		mode = c.ResearchMode
	default:
		return false
	}
	return mode == SourceTermsModeOn && ownerID != "" && (len(c.Users) == 0 || c.Users[ownerID])
}

func (c TermsConfig) sourceModeOn(kind string) bool {
	return (kind == model.DictSourceKB && c.KBMode == SourceTermsModeOn) ||
		(kind == model.DictSourceResearch && c.ResearchMode == SourceTermsModeOn)
}

// TermsSourceStore is the source persistence: the idempotency row and the
// research item. *repository.DynamoDBRepository implements it.
type TermsSourceStore interface {
	GetTermsSourceRun(ctx context.Context, ownerID, kind, ref string) (*model.TermsSourceRun, error)
	PutTermsSourceRun(ctx context.Context, run *model.TermsSourceRun) error
	GetResearchByID(ctx context.Context, researchID string) (*model.Research, error)
}

var _ TermsSourceStore = (*repository.DynamoDBRepository)(nil)

// TermsSourceObjects reads KB bucket objects; *s3.Client implements it.
type TermsSourceObjects interface {
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

// TermsSourceDeps wires the KB/research sources. Bucket is the KB bucket.
type TermsSourceDeps struct {
	Store   TermsSourceStore
	Objects TermsSourceObjects
	Bucket  string
	Publish func(context.Context, model.TermsSourceRequested) error
}

// SetSourceReader installs the KB/research source dependencies; unset, every
// source is skipped as NOT_CONFIGURED.
func (s *TermsAnalysisService) SetSourceReader(deps TermsSourceDeps) {
	s.sources = deps
}

func (s *TermsAnalysisService) sourcesConfigured() bool {
	return s.sources.Store != nil && s.sources.Objects != nil && s.sources.Bucket != ""
}

// TermsSourcePublisher publishes ttobak.analysis/TermsSourceRequested on the
// default bus.
func TermsSourcePublisher(client actionEventClient) func(context.Context, model.TermsSourceRequested) error {
	return func(ctx context.Context, event model.TermsSourceRequested) error {
		detail, _ := json.Marshal(event)
		out, err := client.PutEvents(ctx, &eventbridge.PutEventsInput{Entries: []ebtypes.PutEventsRequestEntry{{
			EventBusName: aws.String("default"), Source: aws.String("ttobak.analysis"),
			DetailType: aws.String("TermsSourceRequested"), Detail: aws.String(string(detail)),
		}}})
		if err == nil && (out == nil || out.FailedEntryCount != 0 || len(out.Entries) != 1 || aws.ToString(out.Entries[0].EventId) == "") {
			err = errors.New("terms source event was not accepted")
		}
		return err
	}
}

// sourceLabel names a source in logs: the research ID, or a digest of any
// other ref (a KB key carries a user's file name).
func sourceLabel(kind, ref string) string {
	if kind == model.DictSourceResearch && attachmentIdentifier.MatchString(ref) {
		return ref
	}
	if ref == "" {
		return "-"
	}
	return model.DictObsSourceID(kind, ref)
}

// kbObjectOwner returns {uid} of a kb/{uid}/{name} key.
func kbObjectOwner(key string) (string, bool) {
	parts := strings.Split(key, "/")
	if len(key) > termsSourceMaxKeyBytes || !utf8.ValidString(key) || len(parts) != 3 || parts[0]+"/" != termsSourceKBPrefix ||
		!attachmentIdentifier.MatchString(parts[1]) || parts[2] == "" || parts[2] == "." || parts[2] == ".." {
		return "", false
	}
	return parts[1], true
}

// researchReportID returns {id} of the full report key shared/research/{id}.md;
// section files (shared/research/{id}/{slug}.md) are not sources.
func researchReportID(key string) (string, bool) {
	rest, ok := strings.CutPrefix(key, termsSourceResearchPrefix)
	if !ok {
		return "", false
	}
	id, ok := strings.CutSuffix(rest, ".md")
	if !ok || !attachmentIdentifier.MatchString(id) {
		return "", false
	}
	return id, true
}

// supportedSourceText reports whether the object is plain text the worker can
// read directly. The attachment extraction service (PDF/PPTX/DOCX) is bound to
// meeting attachments and runs asynchronously, so other formats are
// UNSUPPORTED for now.
func supportedSourceText(key string) bool {
	switch strings.ToLower(path.Ext(key)) {
	case ".md", ".markdown", ".txt":
		return true
	}
	return false
}

// validSourceAccounts keeps well-formed, distinct Account IDs in order.
func validSourceAccounts(ids []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || len(id) > 128 || strings.ContainsAny(id, "#/") || !utf8.ValidString(id) || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func kbObjectAccounts(meta map[string]string) []string {
	if v, ok := meta[kbAccountMetaKey]; ok {
		return validSourceAccounts([]string{v})
	}
	return nil
}

// s3ObjectMissing treats 404 and 403 as a missing object: without
// s3:ListBucket, S3 answers a missing key with 403.
func s3ObjectMissing(err error) bool {
	if err == nil {
		return false
	}
	var noKey *s3types.NoSuchKey
	var notFound *s3types.NotFound
	if errors.As(err, &noKey) || errors.As(err, &notFound) {
		return true
	}
	var resp *awshttp.ResponseError
	if errors.As(err, &resp) && (resp.HTTPStatusCode() == 403 || resp.HTTPStatusCode() == 404) {
		return true
	}
	var api smithy.APIError
	if errors.As(err, &api) {
		switch api.ErrorCode() {
		case "NoSuchKey", "NotFound", "Forbidden", "AccessDenied":
			return true
		}
	}
	return false
}

// withKeyFallback runs read on key as delivered and, only when that object
// is missing and the key looks URL-encoded (contains % or +), once more on
// its url.QueryUnescape form. It returns the key that was read.
func withKeyFallback[T any](key string, read func(string) (T, error)) (T, string, error) {
	out, err := read(key)
	if !s3ObjectMissing(err) || !strings.ContainsAny(key, "%+") {
		return out, key, err
	}
	decoded, decodeErr := url.QueryUnescape(key)
	if decodeErr != nil || decoded == key {
		return out, key, err
	}
	out2, err2 := read(decoded)
	if err2 != nil && s3ObjectMissing(err2) {
		return out, key, err
	}
	return out2, decoded, err2
}

// RequestSourceFromObject maps an Object Created event on the KB bucket to a
// TermsSourceRequested event: kb/{uid}/{name} (Account from the signed
// account-id metadata, read with HeadObject) and shared/research/{id}.md
// (owner and Accounts from the research item). Other keys, buckets and
// disabled kinds are logged and skipped. A returned error is retryable.
func (s *TermsAnalysisService) RequestSourceFromObject(ctx context.Context, bucket, key string) error {
	ev, outcome, err := s.sourceFromObject(ctx, bucket, key)
	if ev != nil {
		switch {
		case s.sources.Publish == nil:
			outcome = TermsSourceNotConfigured
		default:
			if err = s.sources.Publish(ctx, *ev); err != nil {
				outcome = "PUBLISH_FAILED"
			} else {
				outcome = TermsSourceQueued
			}
		}
	}
	kind, ref := "-", ""
	if ev != nil {
		kind, ref = ev.Kind, ev.Ref
	} else if strings.HasPrefix(key, termsSourceKBPrefix) {
		kind, ref = model.DictSourceKB, key
	} else if strings.HasPrefix(key, termsSourceResearchPrefix) {
		kind, ref = model.DictSourceResearch, key
		if id, ok := researchReportID(key); ok {
			ref = id
		}
	}
	log.Printf("terms source trigger: kind=%s src=%s outcome=%s", kind, sourceLabel(kind, ref), outcome)
	return err
}

func (s *TermsAnalysisService) sourceFromObject(ctx context.Context, bucket, key string) (*model.TermsSourceRequested, string, error) {
	if !s.sourcesConfigured() || bucket != s.sources.Bucket {
		return nil, TermsSourceSkipped, nil
	}
	if strings.HasPrefix(key, termsSourceKBPrefix) {
		owner, ok := kbObjectOwner(key)
		switch {
		case !ok:
			return nil, TermsSourceSkipped, nil
		case !s.cfg.SourceEnabled(model.DictSourceKB, owner):
			return nil, model.TermsErrorDisabled, nil
		case !supportedSourceText(key):
			return nil, TermsSourceUnsupported, nil
		}
		head, resolved, err := withKeyFallback(key, func(k string) (*s3.HeadObjectOutput, error) {
			return s.sources.Objects.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(k)})
		})
		if s3ObjectMissing(err) {
			return nil, TermsSourceNotFound, nil
		}
		if err != nil {
			return nil, "HEAD_FAILED", fmt.Errorf("read KB object metadata: %w", err)
		}
		if o, ok := kbObjectOwner(resolved); !ok || o != owner {
			return nil, TermsSourceSkipped, nil
		}
		return &model.TermsSourceRequested{Kind: model.DictSourceKB, Ref: resolved, OwnerID: owner, AccountIDs: kbObjectAccounts(head.Metadata)}, "", nil
	}
	id, ok := researchReportID(key)
	if !ok {
		return nil, TermsSourceSkipped, nil
	}
	if !s.cfg.sourceModeOn(model.DictSourceResearch) {
		return nil, model.TermsErrorDisabled, nil
	}
	research, err := s.sources.Store.GetResearchByID(ctx, id)
	if err != nil {
		return nil, "LOOKUP_FAILED", fmt.Errorf("read research: %w", err)
	}
	if research == nil || !attachmentIdentifier.MatchString(research.UserID) {
		return nil, TermsSourceNotFound, nil
	}
	if !s.cfg.SourceEnabled(model.DictSourceResearch, research.UserID) {
		return nil, model.TermsErrorDisabled, nil
	}
	return &model.TermsSourceRequested{Kind: model.DictSourceResearch, Ref: id, OwnerID: research.UserID, AccountIDs: validSourceAccounts(research.AccountIDs)}, "", nil
}

// termsSourceDoc is the text one source run reads and its Accounts, from the
// authoritative store (object metadata, research item), not the event.
type termsSourceDoc struct {
	text     string
	accounts []string
}

// ProcessSource extracts terms from one KB document or research report.
// Disabled, invalid and unusable sources are logged and dropped; a returned
// error (read, provider or merge failure, research not yet done) asks for
// redelivery. A TERMS_SOURCE row with the content hash makes a repeat of the
// same content a no-op; merges are idempotent per source on their own.
func (s *TermsAnalysisService) ProcessSource(ctx context.Context, ev model.TermsSourceRequested) error {
	label := fmt.Sprintf("terms source: kind=%s src=%s owner=%s", ev.Kind, sourceLabel(ev.Kind, ev.Ref), ev.OwnerID)
	done := func(outcome string, err error) error {
		log.Printf("%s outcome=%s", label, outcome)
		return err
	}
	if !validSourceEvent(ev) {
		return done(TermsSourceInvalid, nil)
	}
	if !s.cfg.SourceEnabled(ev.Kind, ev.OwnerID) {
		return done(model.TermsErrorDisabled, nil)
	}
	if !s.sourcesConfigured() {
		return done(TermsSourceNotConfigured, nil)
	}
	doc, outcome, err := s.loadSource(ctx, ev)
	if outcome != "" {
		return done(outcome, err)
	}
	hash := actionSourceHash(doc.text)
	prior, err := s.sources.Store.GetTermsSourceRun(ctx, ev.OwnerID, ev.Kind, ev.Ref)
	if err != nil {
		return done("RUN_READ_FAILED", err)
	}
	if prior != nil && prior.ContentHash == hash {
		return done(TermsSourceUnchanged, nil)
	}
	candidates, stats, code := s.extractSource(ctx, doc.text)
	switch code {
	case "":
	case model.AnalysisInvalidOutput:
		return done(code, nil) // a model that broke the schema is not retried
	default:
		return done(code, fmt.Errorf("terms source extraction failed: %s", code))
	}
	// No transcript exists to ground aliases in, so heardAs is dropped
	// rather than failing the whole candidate.
	for i := range candidates {
		candidates[i].HeardAs = nil
	}
	filtered, filterCounts := FilterCandidates(candidates, TermSources{Documents: sourceDocumentTexts(doc.text)}, s.cfg.MinConfidence)
	terms := make([]ExtractedTerm, 0, len(filtered))
	for _, f := range filtered {
		terms = append(terms, ExtractedTerm{
			DisplayAs: f.DisplayAs, Phrase: f.Phrase, Type: f.Type, SourceTypes: f.SourceTypes,
			Confidence: f.Confidence, TranscribeKo: f.TranscribeKo, TranscribeEn: f.TranscribeEn,
		})
	}
	refresh, err := s.mergeSource(ctx, label, ev, doc.accounts, terms)
	if err != nil {
		s.refreshVocabularies(ctx, label, refresh) // terms written before the failure are live
		return done("FAILED", err)
	}
	if err := s.sources.Store.PutTermsSourceRun(ctx, &model.TermsSourceRun{
		OwnerID: ev.OwnerID, Kind: ev.Kind, Ref: ev.Ref, ContentHash: hash, UpdatedAt: s.now().UTC().Format(model.DictTimeLayout),
	}); err != nil {
		// The merge stands; a redelivery only repeats the model call.
		log.Printf("%s record=failed", label)
	}
	counts, _ := json.Marshal(filterCounts)
	log.Printf("%s outcome=SUCCEEDED calls=%d inputTokens=%d outputTokens=%d filter=%s", label, stats.calls, stats.inputTokens, stats.outputTokens, counts)
	s.refreshVocabularies(ctx, label, refresh)
	return nil
}

func validSourceEvent(ev model.TermsSourceRequested) bool {
	if !attachmentIdentifier.MatchString(ev.OwnerID) {
		return false
	}
	switch ev.Kind {
	case model.DictSourceKB:
		owner, ok := kbObjectOwner(ev.Ref)
		return ok && owner == ev.OwnerID
	case model.DictSourceResearch:
		return attachmentIdentifier.MatchString(ev.Ref)
	}
	return false
}

// loadSource reads the source text; a non-empty outcome ends the run.
func (s *TermsAnalysisService) loadSource(ctx context.Context, ev model.TermsSourceRequested) (termsSourceDoc, string, error) {
	key := ev.Ref
	var accounts []string
	if ev.Kind == model.DictSourceResearch {
		research, err := s.sources.Store.GetResearchByID(ctx, ev.Ref)
		switch {
		case err != nil:
			return termsSourceDoc{}, "LOOKUP_FAILED", fmt.Errorf("read research: %w", err)
		case research == nil || research.UserID != ev.OwnerID:
			return termsSourceDoc{}, TermsSourceNotFound, nil
		case research.TrashedAt != "":
			return termsSourceDoc{}, TermsSourceSkipped, nil
		case research.Status != model.StatusDone:
			return termsSourceDoc{}, TermsSourceNotReady, errTermsSourceNotReady
		}
		key = research.S3Key
		if !strings.HasPrefix(key, termsSourceResearchPrefix) || strings.Contains(key, "..") || !supportedSourceText(key) {
			return termsSourceDoc{}, TermsSourceNotFound, nil
		}
		accounts = validSourceAccounts(research.AccountIDs)
	} else if !supportedSourceText(key) {
		return termsSourceDoc{}, TermsSourceUnsupported, nil
	}
	// The mapper publishes the key that resolved; the fallback only matters
	// for a hand-published event.
	out, _, err := withKeyFallback(key, func(k string) (*s3.GetObjectOutput, error) {
		return s.sources.Objects.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(s.sources.Bucket), Key: aws.String(k), Range: aws.String(fmt.Sprintf("bytes=0-%d", termsSourceMaxBytes)),
		})
	})
	if s3ObjectMissing(err) {
		return termsSourceDoc{}, TermsSourceNotFound, nil
	}
	if err != nil {
		return termsSourceDoc{}, "READ_FAILED", fmt.Errorf("read source object: %w", err)
	}
	defer out.Body.Close()
	body, err := io.ReadAll(io.LimitReader(out.Body, termsSourceMaxBytes+1))
	if err != nil {
		return termsSourceDoc{}, "READ_FAILED", fmt.Errorf("read source object: %w", err)
	}
	if ev.Kind == model.DictSourceKB {
		// The signed upload metadata is the Account choice (L4).
		accounts = kbObjectAccounts(out.Metadata)
	}
	text := truncateUTF8(string(body), termsSourceMaxBytes)
	if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return termsSourceDoc{}, TermsSourceUnsupported, nil
	}
	if strings.TrimSpace(text) == "" {
		return termsSourceDoc{}, "NO_TEXT", nil
	}
	return termsSourceDoc{text: text, accounts: accounts}, "", nil
}

// sourceDocuments splits the capped text at line boundaries into documents of
// at most 16 KiB, as owner documents are sent on the meeting path.
func sourceDocuments(text string) []termsDocumentText {
	var docs []termsDocumentText
	total := 0
	for _, chunk := range chunkTranscript(text, termsDocumentMaxBytes) {
		if total+len(chunk) > termsSourceMaxBytes {
			break
		}
		total += len(chunk)
		docs = append(docs, termsDocumentText{ID: fmt.Sprintf("source-%d", len(docs)+1), Text: chunk})
	}
	return docs
}

func sourceDocumentTexts(text string) []TermSourceDocumentText {
	var out []TermSourceDocumentText
	for _, d := range sourceDocuments(text) {
		out = append(out, TermSourceDocumentText{ID: d.ID, Text: d.Text})
	}
	return out
}

// extractSource runs one model call with the document text and no transcript.
func (s *TermsAnalysisService) extractSource(ctx context.Context, text string) ([]TermCandidate, termsRunStats, string) {
	var stats termsRunStats
	request, err := buildTermsRequest(termsCallInput{Task: "extract_terms", Call: 1, Calls: 1, Documents: sourceDocuments(text)})
	if err != nil {
		return nil, stats, model.AnalysisInvalidOutput
	}
	callCtx, cancel := context.WithTimeout(ctx, termsCallTimeout)
	defer cancel()
	out, in, outTokens, err := s.model.InvokeTermsModel(callCtx, request, s.cfg.ModelID)
	stats.calls, stats.inputTokens, stats.outputTokens = 1, in, outTokens
	if err != nil {
		if errors.Is(err, ErrInvalidAnalysisResponse) {
			return nil, stats, model.AnalysisInvalidOutput
		}
		if ctx.Err() != nil {
			return nil, stats, model.AnalysisInterrupted
		}
		return nil, stats, model.AnalysisProviderFailed
	}
	candidates, err := decodeTermsOutput(out)
	if err != nil {
		return nil, stats, model.AnalysisInvalidOutput
	}
	return candidates, stats, ""
}

// mergeSource merges into each Account the owner is a current member of (the
// early check saves the writes; the transaction's ConditionCheck remains the
// authority), or into the owner's Personal layer when no Account took the
// terms. It returns the users whose vocabularies should refresh.
func (s *TermsAnalysisService) mergeSource(ctx context.Context, label string, ev model.TermsSourceRequested, accounts []string, terms []ExtractedTerm) ([]string, error) {
	if len(terms) == 0 {
		log.Printf("%s layer=none outcome=NO_TERMS", label)
		return nil, nil
	}
	ref := TermsSourceRef{Kind: ev.Kind, Ref: ev.Ref, OwnerID: ev.OwnerID}
	logMerge := func(layer, outcome string, c model.TermsCounters) {
		log.Printf("%s layer=%s outcome=%s created=%d suggested=%d merged=%d promoted=%d skipped=%d evicted=%d",
			label, layer, outcome, c.Created, c.Suggested, c.Merged, c.Promoted, c.SkippedTombstone+c.SkippedManual+c.SkippedTainted, c.Evicted)
	}
	var refresh []string
	merged := 0
	for _, accountID := range accounts {
		layer := "account account=" + accountID
		if s.listAccountMembers == nil {
			return refresh, errors.New("terms source: account member lister is not configured")
		}
		members, err := s.listAccountMembers(ctx, accountID)
		if err != nil {
			log.Printf("%s layer=%s outcome=MEMBERS_FAILED", label, layer)
			return refresh, fmt.Errorf("list account members: %w", err)
		}
		isMember := false
		for _, m := range members {
			isMember = isMember || m == ev.OwnerID
		}
		if !isMember {
			log.Printf("%s layer=%s outcome=NOT_MEMBER", label, layer)
			continue
		}
		res, err := s.merger.MergeScoped(ctx, TermsScope{
			PK:     model.PrefixAccount + accountID,
			Member: &repository.MembershipCheck{AccountID: accountID, UserID: ev.OwnerID},
		}, terms, ref)
		if errors.Is(err, repository.ErrNotAccountMember) {
			log.Printf("%s layer=%s outcome=NOT_MEMBER", label, layer)
			continue
		}
		if res != nil && madeActive(res.Counters) {
			refresh = append(append(refresh, ev.OwnerID), members...)
		}
		if err != nil {
			if res != nil {
				logMerge(layer, "FAILED", res.Counters)
			}
			return refresh, err
		}
		merged++
		logMerge(layer, "MERGED", res.Counters)
	}
	if merged > 0 {
		return refresh, nil
	}
	// Personal fallback: no Account, or the owner is not a member of any.
	if err := s.migrateLegacy(ctx, ev.OwnerID); err != nil {
		return refresh, err
	}
	res, err := s.merger.MergeScoped(ctx, TermsScope{PK: model.PrefixUser + ev.OwnerID}, terms, ref)
	if res != nil && madeActive(res.Counters) {
		refresh = append(refresh, ev.OwnerID)
	}
	if err != nil {
		if res != nil {
			logMerge("personal", "FAILED", res.Counters)
		}
		return refresh, err
	}
	logMerge("personal", "MERGED", res.Counters)
	return refresh, nil
}
