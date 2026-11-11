package service

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// Daily AWS technical document terms (ADR-008 L4). An EventBridge schedule at
// 21:30 UTC, after the 19:00 crawl, invokes summarize with
// {"kind":"tech-daily"}; ProcessTechDaily reads the titles and summaries of
// the technical documents the reserved __tech__ crawler wrote in the UTC day
// ending at 21:00 (the news day), runs the source extraction prompt, and
// merges into DICT#COMMON only the terms named by at least two distinct
// technical documents within the seven days ending that day. The news rule
// counts distinct crawler sources; tech documents all come from one
// operator-reserved source, so the distinct-document rule replaces it, and
// only documents on an official AWS host (aws.amazon.com and its
// subdomains, https) are read: the crawler's web-search results from other
// sites never count. Only Hangul or ASCII-Latin terms qualify (the
// multilingual titles may yield other scripts, which Transcribe cannot take
// either). The writer is model.DictTechActor with its own source kind, so a
// term named by news and tech on the same day is two observations; creations
// and evictions are audited and user-edited Common terms are left alone by
// the merge rules, as for news. Logs carry the day, codes and counts only.
const (
	// techMinDocuments is the distinct-document threshold over the window.
	techMinDocuments = 2
	// techS3Prefix is where tech_crawler.py stores document bodies; a row
	// whose key is elsewhere did not come from that writer and is skipped.
	techS3Prefix = "shared/aws-docs/"
)

// TermsTechDocuments lists crawled AWS technical documents by crawl time;
// *repository.CrawlerRepository implements it.
type TermsTechDocuments interface {
	ListTechDocumentsCrawledBetween(ctx context.Context, from, to int64) ([]model.CrawledDocument, error)
}

var _ TermsTechDocuments = (*repository.CrawlerRepository)(nil)

// TermsTechStore keeps the tech candidate history and the day's run row;
// *repository.DynamoDBRepository implements it.
type TermsTechStore interface {
	GetTermsTechHistory(ctx context.Context, termKey string) (*model.TermsTechHistory, error)
	PutTermsTechHistory(ctx context.Context, h *model.TermsTechHistory) error
	GetTermsTechRun(ctx context.Context, day string) (*model.TermsTechRun, error)
	PutTermsTechRun(ctx context.Context, run *model.TermsTechRun) error
}

var _ TermsTechStore = (*repository.DynamoDBRepository)(nil)

// TermsTechDeps wires the daily tech run.
type TermsTechDeps struct {
	Documents TermsTechDocuments
	Store     TermsTechStore
}

// SetTechReader installs the tech dependencies; unset, every run is skipped
// as NOT_CONFIGURED.
func (s *TermsAnalysisService) SetTechReader(deps TermsTechDeps) {
	s.tech = deps
}

// officialAWSDocumentURL accepts an https URL on aws.amazon.com or one of its
// subdomains (docs.aws.amazon.com), without credentials or a port.
func officialAWSDocumentURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "aws.amazon.com" || strings.HasSuffix(host, ".aws.amazon.com")
}

// techS3KeyOf reports whether key is the body key tech_crawler.py writes for
// hash: shared/aws-docs/{service}/{hash}.md with one plain service segment.
func techS3KeyOf(key, hash string) bool {
	rest, ok := strings.CutPrefix(key, techS3Prefix)
	if !ok {
		return false
	}
	service, file, ok := strings.Cut(rest, "/")
	return ok && service != "" && service != "." && service != ".." && file == hash+".md"
}

// techDocuments keeps the reserved tech source's documents on an official
// AWS host with a well-formed hash and body key, as title + summary capped at
// newsDocumentMaxBytes, each hash once, in crawl order. excluded counts the
// rows skipped for any of these reasons.
func techDocuments(rows []model.CrawledDocument) ([]newsDocument, int) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].CrawledAt != rows[j].CrawledAt {
			return rows[i].CrawledAt < rows[j].CrawledAt
		}
		return rows[i].DocHash < rows[j].DocHash
	})
	var out []newsDocument
	seen := map[string]bool{}
	excluded := 0
	for _, row := range rows {
		if row.SourceID != techLinkSourceID || row.Type != "tech" || (row.QualityStatus != "" && row.QualityStatus != "PUBLISHED") ||
			!attachmentIdentifier.MatchString(row.DocHash) || !techS3KeyOf(row.S3Key, row.DocHash) || !officialAWSDocumentURL(row.URL) {
			excluded++
			continue
		}
		text := strings.TrimSpace(strings.TrimSpace(row.Title) + "\n" + strings.TrimSpace(row.Summary))
		text = truncateUTF8(text, newsDocumentMaxBytes)
		if text == "" || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
			excluded++
			continue
		}
		if seen[row.DocHash] {
			continue
		}
		seen[row.DocHash] = true
		out = append(out, newsDocument{hash: row.DocHash, source: techLinkSourceID, text: text, match: newMatchText(text)})
	}
	return out, excluded
}

// ProcessTechDaily extracts terms from the AWS technical documents of day
// (YYYY-MM-DD, the UTC day ending at 21:00) into the Common layer, as
// ProcessNewsDaily does for news: a disabled, invalid or unconfigured run is
// logged and dropped; a returned error asks for redelivery, which repeats
// only the model calls (history documents are a set and merges are
// idempotent per day); a run row with the content hash makes a repeat of the
// same documents a no-op. Per-user vocabularies are not rebuilt.
func (s *TermsAnalysisService) ProcessTechDaily(ctx context.Context, day string) error {
	label := "terms tech: day=" + day
	done := func(outcome string, err error) error {
		log.Printf("%s outcome=%s", label, outcome)
		return err
	}
	if s.cfg.TechMode != SourceTermsModeOn {
		return done(model.TermsErrorDisabled, nil)
	}
	from, to, ok := newsWindow(day)
	if !ok {
		label = "terms tech: day=-"
		return done(TermsSourceInvalid, nil)
	}
	if s.tech.Documents == nil || s.tech.Store == nil {
		return done(TermsSourceNotConfigured, nil)
	}
	rows, err := s.tech.Documents.ListTechDocumentsCrawledBetween(ctx, from, to)
	if err != nil {
		return done("READ_FAILED", fmt.Errorf("list tech documents: %w", err))
	}
	docs, excluded := techDocuments(rows)
	if excluded > 0 {
		log.Printf("%s excluded=%d", label, excluded)
	}
	if len(docs) == 0 {
		return done("NO_DOCUMENTS", nil)
	}
	hash := newsContentHash(docs)
	prior, err := s.tech.Store.GetTermsTechRun(ctx, day)
	if err != nil {
		return done("RUN_READ_FAILED", err)
	}
	if prior != nil && prior.ContentHash == hash {
		return done(TermsSourceUnchanged, nil)
	}

	batches, dropped := documentBatches(docs, "tech")
	candidates, stats, invalid, code := s.extractDailyCandidates(ctx, batches)
	if code != "" {
		return done(code, fmt.Errorf("tech terms extraction failed: %s", code))
	}

	// Ground each candidate in today's documents, then apply the
	// distinct-document threshold against the candidate history: documents
	// within the seven days ending at day count; later ones (a re-run of an
	// older day) are kept, older ones dropped.
	now := s.now().UTC()
	start, _ := time.Parse(newsDayLayout, day) // validated by newsWindow
	oldest := start.AddDate(0, 0, -(newsRepeatDays - 1)).Format(newsDayLayout)
	var kept []newsCandidate
	ungrounded, foreign := 0, 0
	for _, c := range candidates {
		if ko, en := transcribeEligibility(strings.TrimSpace(c.term.DisplayAs)); !ko && !en {
			foreign++
			continue
		}
		display := newMatchText(c.term.DisplayAs)
		for _, d := range docs {
			if d.match.count(display, true) > 0 {
				c.docs = append(c.docs, d.hash)
			}
		}
		if len(c.docs) == 0 {
			ungrounded++
			continue
		}
		h, err := s.tech.Store.GetTermsTechHistory(ctx, c.term.TermKey)
		if err != nil {
			return done("HISTORY_FAILED", fmt.Errorf("read tech history: %w", err))
		}
		var sightings []string
		if h != nil {
			for _, e := range h.Documents {
				if d, doc, ok := strings.Cut(e, "#"); ok && d >= oldest && validDay(d) && attachmentIdentifier.MatchString(doc) {
					sightings = append(sightings, e)
				}
			}
		}
		for _, doc := range c.docs {
			sightings = append(sightings, newsSighting(day, doc))
		}
		sightings = unionStrings(nil, sightings)
		sort.Strings(sightings)
		if len(sightings) > newsHistoryMaxSightings {
			sightings = sightings[len(sightings)-newsHistoryMaxSightings:]
		}
		next := &model.TermsTechHistory{
			PK: model.PrefixDictCommon, SK: model.TermsTechSK(c.term.TermKey), TermKey: c.term.TermKey,
			Documents: sightings, ExpiresAt: now.Add(newsHistoryTTL).Unix(), UpdatedAt: now.Format(model.DictTimeLayout),
		}
		window := map[string]bool{}
		for _, e := range sightings {
			d, doc, _ := strings.Cut(e, "#")
			next.Days = unionStrings(next.Days, []string{d})
			if d <= day {
				window[doc] = true
			}
		}
		if err := s.tech.Store.PutTermsTechHistory(ctx, next); err != nil {
			return done("HISTORY_FAILED", fmt.Errorf("write tech history: %w", err))
		}
		if len(window) >= techMinDocuments {
			kept = append(kept, c)
		}
	}

	counters, err := s.mergeCommonDay(ctx, "terms tech:", TermsSourceRef{Kind: model.DictSourceTech, Ref: day, OwnerID: model.DictTechActor}, kept)
	logCounts := func(outcome string) {
		log.Printf("%s outcome=%s docs=%d dropped=%d calls=%d inputTokens=%d outputTokens=%d invalid=%d candidates=%d foreign=%d ungrounded=%d kept=%d created=%d merged=%d skipped=%d evicted=%d",
			label, outcome, len(docs), dropped, stats.calls, stats.inputTokens, stats.outputTokens, invalid, len(candidates), foreign, ungrounded, len(kept),
			counters.Created, counters.Merged+counters.AlreadyCounted, counters.SkippedTombstone+counters.SkippedManual+counters.SkippedTainted+counters.Capped, counters.Evicted)
	}
	if err != nil {
		logCounts("FAILED")
		return err
	}
	if invalid == 0 {
		// A day with a broken model answer stays open for a later repeat.
		if err := s.tech.Store.PutTermsTechRun(ctx, &model.TermsTechRun{
			Day: day, ContentHash: hash, ExpiresAt: now.Add(newsHistoryTTL).Unix(), UpdatedAt: now.Format(model.DictTimeLayout),
		}); err != nil {
			log.Printf("%s record=failed", label) // the merge stands
		}
	}
	logCounts("SUCCEEDED")
	return nil
}
