package service

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// Daily news terms (layered dictionary 4.2, Task 11). An EventBridge schedule
// at 21:00 UTC, after the 19:00 crawl, invokes summarize with
// {"kind":"news-daily"}; ProcessNewsDaily reads the titles and summaries of
// the news documents crawled in the UTC day ending then, runs the source
// extraction prompt, and merges into DICT#COMMON only the terms seen in at
// least two distinct crawler sources that day, or on at least two distinct
// days of the last seven with at least two distinct sources among them: one
// source alone never qualifies a term. Common rows are visible to every user
// (R6, R15), so only searched (not custom-URL) documents of operator-curated
// global sources are read (repository.IsGlobalNewsSource; anything unknown is
// excluded), the only evidence on a term is the public document hash, the
// writer is model.DictNewsActor, creations and evictions are audited (R14),
// and user-edited Common terms are left alone by the merge rules. Logs carry
// the day, codes and counts only.
const (
	// newsDayCutoffHour is the UTC hour the daily run fires and its day ends.
	newsDayCutoffHour = 21
	// newsDayGrace lets a trigger a little before the cutoff still name the
	// day that is ending.
	newsDayGrace = 10 * time.Minute
	// newsDocumentMaxBytes caps one document's title + summary (crawler
	// summaries are at most 1500 characters).
	newsDocumentMaxBytes = 6 * 1024
	// newsMaxCalls bounds the model calls of one day; each call carries at
	// most termsSourceMaxBytes, as a KB or research source does.
	newsMaxCalls = 4
	// newsRepeatDays is the window of the distinct-days threshold.
	newsRepeatDays = 7
	// newsMinRepeats is the threshold for both sources and days.
	newsMinRepeats = 2
	// newsHistoryTTL is how long candidate history and run rows are kept.
	newsHistoryTTL = 14 * 24 * time.Hour
	// newsHistoryMaxSightings bounds a candidate's day#source entries.
	newsHistoryMaxSightings = 50
	// newsIngestSearch marks a document found by the source's searches; a
	// "custom" one came from a URL a user configured and is never read.
	newsIngestSearch = "search"

	newsDayLayout = "2006-01-02"
)

// TermsNewsDocuments lists crawled news documents by crawl time and tells
// operator-curated global sources apart; *repository.CrawlerRepository
// implements it.
type TermsNewsDocuments interface {
	ListDocumentsCrawledBetween(ctx context.Context, from, to int64) ([]model.CrawledDocument, error)
	IsGlobalNewsSource(ctx context.Context, sourceID string) (bool, error)
}

var _ TermsNewsDocuments = (*repository.CrawlerRepository)(nil)

// TermsNewsStore keeps the candidate history and the day's run row;
// *repository.DynamoDBRepository implements it.
type TermsNewsStore interface {
	GetTermsNewsHistory(ctx context.Context, termKey string) (*model.TermsNewsHistory, error)
	PutTermsNewsHistory(ctx context.Context, h *model.TermsNewsHistory) error
	GetTermsNewsRun(ctx context.Context, day string) (*model.TermsNewsRun, error)
	PutTermsNewsRun(ctx context.Context, run *model.TermsNewsRun) error
}

var _ TermsNewsStore = (*repository.DynamoDBRepository)(nil)

// TermsNewsDeps wires the daily news run.
type TermsNewsDeps struct {
	Documents TermsNewsDocuments
	Store     TermsNewsStore
}

// SetNewsReader installs the news dependencies; unset, every run is skipped
// as NOT_CONFIGURED.
func (s *TermsAnalysisService) SetNewsReader(deps TermsNewsDeps) {
	s.news = deps
}

// NewsDayFor names the day a run triggered at t processes: the UTC date of
// the most recent 21:00 cutoff at or before t (plus a short grace), so a
// delayed retry after midnight still processes the day that ended at 21:00.
func NewsDayFor(t time.Time) string {
	t = t.UTC().Add(newsDayGrace)
	cutoff := time.Date(t.Year(), t.Month(), t.Day(), newsDayCutoffHour, 0, 0, 0, time.UTC)
	if t.Before(cutoff) {
		cutoff = cutoff.AddDate(0, 0, -1)
	}
	return cutoff.Format(newsDayLayout)
}

// newsWindow returns [from, to) in Unix seconds of day: the 24 hours ending at
// 21:00 UTC on that date.
func newsWindow(day string) (int64, int64, bool) {
	d, err := time.Parse(newsDayLayout, day)
	if err != nil || d.Format(newsDayLayout) != day {
		return 0, 0, false
	}
	end := d.Add(newsDayCutoffHour * time.Hour)
	return end.Add(-24 * time.Hour).Unix(), end.Unix(), true
}

type newsDocument struct {
	hash   string
	source string // crawler source ID (from the item's partition key)
	text   string
	match  matchText
}

// newsDocuments keeps published, searched news documents with a well-formed
// hash and source ID, once each, as title + summary capped at
// newsDocumentMaxBytes, in crawl order. Legacy rows without an ingest
// source cannot be told from custom-URL ones and are skipped.
func newsDocuments(rows []model.CrawledDocument) []newsDocument {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].CrawledAt != rows[j].CrawledAt {
			return rows[i].CrawledAt < rows[j].CrawledAt
		}
		return rows[i].DocHash < rows[j].DocHash
	})
	seen := map[string]bool{}
	var out []newsDocument
	for _, row := range rows {
		if row.QualityStatus != "PUBLISHED" || row.IngestSource != newsIngestSearch || (row.Type != "" && row.Type != "news") ||
			!attachmentIdentifier.MatchString(row.DocHash) || !validNewsSourceID(row.SourceID) || seen[row.DocHash] {
			continue
		}
		text := strings.TrimSpace(strings.TrimSpace(row.Title) + "\n" + strings.TrimSpace(row.Summary))
		text = truncateUTF8(text, newsDocumentMaxBytes)
		if text == "" || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
			continue
		}
		seen[row.DocHash] = true
		out = append(out, newsDocument{hash: row.DocHash, source: row.SourceID, text: text, match: newMatchText(text)})
	}
	return out
}

// globalNewsDocuments keeps the documents of global sources, looking each
// distinct source up once. A lookup failure is returned (retryable).
func (s *TermsAnalysisService) globalNewsDocuments(ctx context.Context, docs []newsDocument) ([]newsDocument, int, error) {
	global := map[string]bool{}
	var out []newsDocument
	excluded := 0
	for _, d := range docs {
		ok, cached := global[d.source]
		if !cached {
			var err error
			if ok, err = s.news.Documents.IsGlobalNewsSource(ctx, d.source); err != nil {
				return nil, 0, err
			}
			global[d.source] = ok
		}
		if !ok {
			excluded++
			continue
		}
		out = append(out, d)
	}
	return out, excluded, nil
}

// newsSighting is one history entry: a day and a source that named the term.
func newsSighting(day, source string) string { return day + "#" + source }

// newsBatches packs documents into at most newsMaxCalls calls of at most
// termsSourceMaxBytes; documents past the last call are dropped (counted).
func newsBatches(docs []newsDocument) ([][]termsDocumentText, int) {
	var batches [][]termsDocumentText
	size := 0
	for i, d := range docs {
		if len(batches) == 0 || size+len(d.text) > termsSourceMaxBytes {
			if len(batches) == newsMaxCalls {
				return batches, len(docs) - i
			}
			batches = append(batches, nil)
			size = 0
		}
		size += len(d.text)
		batches[len(batches)-1] = append(batches[len(batches)-1], termsDocumentText{ID: fmt.Sprintf("news-%d", i+1), Text: d.text})
	}
	return batches, 0
}

func newsContentHash(docs []newsDocument) string {
	var b strings.Builder
	for _, d := range docs {
		b.WriteString(d.source)
		b.WriteByte(0)
		b.WriteString(d.hash)
		b.WriteByte(0)
		b.WriteString(d.text)
		b.WriteByte(0)
	}
	return actionSourceHash(b.String())
}

type newsCandidate struct {
	term    FilteredTerm
	docs    []string // hashes of today's documents naming the term
	sources []string // their distinct source IDs
}

// ProcessNewsDaily extracts terms from the news documents of day (YYYY-MM-DD,
// the UTC day ending at 21:00) into the Common layer. A disabled, invalid or
// unconfigured run is logged and dropped; a returned error (read, provider,
// history or merge failure) asks for redelivery, which repeats only the model
// calls: history days are a set and merges are idempotent per document. A run
// row with the content hash makes a repeat of the same documents a no-op.
// Per-user vocabularies are not rebuilt (that would fan out to every user);
// they take Common terms at their next scheduled ensure.
func (s *TermsAnalysisService) ProcessNewsDaily(ctx context.Context, day string) error {
	label := "terms news: day=" + day
	done := func(outcome string, err error) error {
		log.Printf("%s outcome=%s", label, outcome)
		return err
	}
	if s.cfg.NewsMode != SourceTermsModeOn {
		return done(model.TermsErrorDisabled, nil)
	}
	from, to, ok := newsWindow(day)
	if !ok {
		label = "terms news: day=-"
		return done(TermsSourceInvalid, nil)
	}
	if s.news.Documents == nil || s.news.Store == nil {
		return done(TermsSourceNotConfigured, nil)
	}
	rows, err := s.news.Documents.ListDocumentsCrawledBetween(ctx, from, to)
	if err != nil {
		return done("READ_FAILED", fmt.Errorf("list news documents: %w", err))
	}
	docs, excluded, err := s.globalNewsDocuments(ctx, newsDocuments(rows))
	if err != nil {
		return done("SOURCE_READ_FAILED", fmt.Errorf("read news source: %w", err))
	}
	if excluded > 0 {
		log.Printf("%s excludedNonGlobal=%d", label, excluded)
	}
	if len(docs) == 0 {
		return done("NO_DOCUMENTS", nil)
	}
	hash := newsContentHash(docs)
	prior, err := s.news.Store.GetTermsNewsRun(ctx, day)
	if err != nil {
		return done("RUN_READ_FAILED", err)
	}
	if prior != nil && prior.ContentHash == hash {
		return done(TermsSourceUnchanged, nil)
	}

	batches, dropped := newsBatches(docs)
	var stats termsRunStats
	invalid := 0
	byKey := map[string]int{}
	var candidates []newsCandidate
	for i, batch := range batches {
		raw, callStats, code := s.extractDocuments(ctx, batch, i+1, len(batches))
		stats.calls += callStats.calls
		stats.inputTokens += callStats.inputTokens
		stats.outputTokens += callStats.outputTokens
		switch code {
		case "":
		case model.AnalysisInvalidOutput:
			invalid++ // a model that broke the schema is not retried
			continue
		default:
			return done(code, fmt.Errorf("news terms extraction failed: %s", code))
		}
		// No transcript grounds aliases: heardAs is dropped, as for KB sources.
		for j := range raw {
			raw[j].HeardAs = nil
		}
		texts := make([]TermSourceDocumentText, 0, len(batch))
		for _, d := range batch {
			texts = append(texts, TermSourceDocumentText{ID: d.ID, Text: d.Text})
		}
		filtered, _ := FilterCandidates(raw, TermSources{Documents: texts}, s.cfg.MinConfidence)
		for _, f := range filtered {
			if at, ok := byKey[f.TermKey]; ok {
				prev := &candidates[at].term
				sources := unionStrings(prev.SourceTypes, f.SourceTypes)
				if betterTerm(f, *prev) {
					*prev = f
				}
				prev.SourceTypes = sources
				continue
			}
			byKey[f.TermKey] = len(candidates)
			candidates = append(candidates, newsCandidate{term: f})
		}
	}

	// Count today's documents and sources naming each candidate, then apply
	// the repeat threshold against the candidate history. Sightings within
	// the seven days ending at day count; later ones (a re-run of an older
	// day) are kept, older ones dropped.
	now := s.now().UTC()
	start, _ := time.Parse(newsDayLayout, day) // validated by newsWindow
	oldest := start.AddDate(0, 0, -(newsRepeatDays - 1)).Format(newsDayLayout)
	var kept []newsCandidate
	ungrounded := 0
	for _, c := range candidates {
		display := newMatchText(c.term.DisplayAs)
		for _, d := range docs {
			if d.match.count(display, true) > 0 {
				c.docs = append(c.docs, d.hash)
				c.sources = unionStrings(c.sources, []string{d.source})
			}
		}
		if len(c.docs) == 0 {
			ungrounded++
			continue
		}
		h, err := s.news.Store.GetTermsNewsHistory(ctx, c.term.TermKey)
		if err != nil {
			return done("HISTORY_FAILED", fmt.Errorf("read news history: %w", err))
		}
		var sightings []string
		if h != nil {
			for _, e := range h.Sources {
				if d, src, ok := strings.Cut(e, "#"); ok && d >= oldest && validNewsSourceID(src) {
					sightings = append(sightings, e)
				}
			}
		}
		for _, src := range c.sources {
			sightings = append(sightings, newsSighting(day, src))
		}
		sightings = unionStrings(nil, sightings)
		sort.Strings(sightings)
		if len(sightings) > newsHistoryMaxSightings {
			sightings = sightings[len(sightings)-newsHistoryMaxSightings:]
		}
		next := &model.TermsNewsHistory{
			PK: model.PrefixDictCommon, SK: model.TermsNewsSK(c.term.TermKey), TermKey: c.term.TermKey,
			Sources: sightings, ExpiresAt: now.Add(newsHistoryTTL).Unix(), UpdatedAt: now.Format(model.DictTimeLayout),
		}
		windowDays, windowSources := map[string]bool{}, map[string]bool{}
		for _, e := range sightings {
			d, src, _ := strings.Cut(e, "#")
			next.Days = unionStrings(next.Days, []string{d})
			if d <= day {
				windowDays[d], windowSources[src] = true, true
			}
		}
		if err := s.news.Store.PutTermsNewsHistory(ctx, next); err != nil {
			return done("HISTORY_FAILED", fmt.Errorf("write news history: %w", err))
		}
		if len(c.sources) >= newsMinRepeats || (len(windowDays) >= newsMinRepeats && len(windowSources) >= newsMinRepeats) {
			kept = append(kept, c)
		}
	}

	counters, err := s.mergeNews(ctx, docs, kept)
	logCounts := func(outcome string) {
		log.Printf("%s outcome=%s docs=%d dropped=%d calls=%d inputTokens=%d outputTokens=%d invalid=%d candidates=%d ungrounded=%d kept=%d created=%d merged=%d skipped=%d evicted=%d",
			label, outcome, len(docs), dropped, stats.calls, stats.inputTokens, stats.outputTokens, invalid, len(candidates), ungrounded, len(kept),
			counters.Created, counters.Merged+counters.AlreadyCounted, counters.SkippedTombstone+counters.SkippedManual+counters.SkippedTainted+counters.Capped, counters.Evicted)
	}
	if err != nil {
		logCounts("FAILED")
		return err
	}
	if invalid == 0 {
		// A day with a broken model answer stays open for a later repeat.
		if err := s.news.Store.PutTermsNewsRun(ctx, &model.TermsNewsRun{
			Day: day, ContentHash: hash, ExpiresAt: now.Add(newsHistoryTTL).Unix(), UpdatedAt: now.Format(model.DictTimeLayout),
		}); err != nil {
			log.Printf("%s record=failed", label) // the merge stands
		}
	}
	logCounts("SUCCEEDED")
	return nil
}

// mergeNews merges each document's kept terms into Common with that
// document's hash as the source, so a term in three documents gets three
// observations and hits.
func (s *TermsAnalysisService) mergeNews(ctx context.Context, docs []newsDocument, kept []newsCandidate) (model.TermsCounters, error) {
	var total model.TermsCounters
	for _, d := range docs {
		var terms []ExtractedTerm
		for _, c := range kept {
			for _, h := range c.docs {
				if h == d.hash {
					f := c.term
					terms = append(terms, ExtractedTerm{
						DisplayAs: f.DisplayAs, Phrase: f.Phrase, Type: f.Type, SourceTypes: f.SourceTypes,
						Confidence: f.Confidence, TranscribeKo: f.TranscribeKo, TranscribeEn: f.TranscribeEn,
					})
					break
				}
			}
		}
		if len(terms) == 0 {
			continue
		}
		res, err := s.merger.MergeScoped(ctx, TermsScope{PK: model.PrefixDictCommon}, terms,
			TermsSourceRef{Kind: model.DictSourceNews, Ref: d.hash, OwnerID: model.DictNewsActor})
		if res != nil {
			addTermsCounters(&total, res.Counters)
		}
		if err != nil {
			return total, err
		}
		if res.CleanupErr != nil {
			log.Printf("terms news: cleanup=failed")
		}
	}
	return total, nil
}

func addTermsCounters(total *model.TermsCounters, c model.TermsCounters) {
	total.Created += c.Created
	total.Merged += c.Merged
	total.AlreadyCounted += c.AlreadyCounted
	total.SkippedTombstone += c.SkippedTombstone
	total.SkippedManual += c.SkippedManual
	total.SkippedTainted += c.SkippedTainted
	total.Capped += c.Capped
	total.Evicted += c.Evicted
}
