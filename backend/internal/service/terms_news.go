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
// least two documents that day or on at least two distinct days of the last
// seven. Common rows are visible to every user (R6), so the only evidence they
// carry is the public document hash, the writer is model.DictNewsActor, and
// user-edited Common terms are left alone by the merge rules. Logs carry the
// day, codes and counts only.
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
	// newsMinRepeats is the threshold for both documents and days.
	newsMinRepeats = 2
	// newsHistoryTTL is how long candidate history and run rows are kept.
	newsHistoryTTL = 14 * 24 * time.Hour
	// newsHistoryMaxDocs keeps the newest document hashes of a candidate.
	newsHistoryMaxDocs = 20

	newsDayLayout = "2006-01-02"
)

// TermsNewsDocuments lists crawled news documents by crawl time;
// *repository.CrawlerRepository implements it.
type TermsNewsDocuments interface {
	ListDocumentsCrawledBetween(ctx context.Context, from, to int64) ([]model.CrawledDocument, error)
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
	hash  string
	text  string
	match matchText
}

// newsDocuments keeps published (or legacy, status-less) news documents with
// a well-formed hash, once each, as title + summary capped at
// newsDocumentMaxBytes, in crawl order.
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
		if (row.QualityStatus != "" && row.QualityStatus != "PUBLISHED") || (row.Type != "" && row.Type != "news") ||
			!attachmentIdentifier.MatchString(row.DocHash) || seen[row.DocHash] {
			continue
		}
		text := strings.TrimSpace(strings.TrimSpace(row.Title) + "\n" + strings.TrimSpace(row.Summary))
		text = truncateUTF8(text, newsDocumentMaxBytes)
		if text == "" || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
			continue
		}
		seen[row.DocHash] = true
		out = append(out, newsDocument{hash: row.DocHash, text: text, match: newMatchText(text)})
	}
	return out
}

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
		b.WriteString(d.hash)
		b.WriteByte(0)
		b.WriteString(d.text)
		b.WriteByte(0)
	}
	return actionSourceHash(b.String())
}

type newsCandidate struct {
	term FilteredTerm
	docs []string // hashes of today's documents naming the term
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
	docs := newsDocuments(rows)
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

	// Count today's documents naming each candidate, then apply the repeat
	// threshold against the candidate history: days within the seven ending
	// at day count; days after it (a re-run of an older day) are kept, older
	// ones dropped.
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
		next := &model.TermsNewsHistory{
			PK: model.PrefixDictCommon, SK: model.TermsNewsSK(c.term.TermKey), TermKey: c.term.TermKey,
			Days: []string{day}, ExpiresAt: now.Add(newsHistoryTTL).Unix(), UpdatedAt: now.Format(model.DictTimeLayout),
		}
		if h != nil {
			for _, d := range h.Days {
				if d >= oldest {
					next.Days = append(next.Days, d)
				}
			}
			next.Docs = h.Docs
		}
		next.Days = unionStrings(nil, next.Days)
		sort.Strings(next.Days)
		next.Docs = unionStrings(next.Docs, c.docs)
		if len(next.Docs) > newsHistoryMaxDocs {
			next.Docs = next.Docs[len(next.Docs)-newsHistoryMaxDocs:]
		}
		if err := s.news.Store.PutTermsNewsHistory(ctx, next); err != nil {
			return done("HISTORY_FAILED", fmt.Errorf("write news history: %w", err))
		}
		days := 0
		for _, d := range next.Days {
			if d <= day {
				days++
			}
		}
		if len(c.docs) >= newsMinRepeats || days >= newsMinRepeats {
			kept = append(kept, c)
		}
	}

	counters, err := s.mergeNews(ctx, day, kept)
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

// mergeNews merges the day's kept terms into Common in one call with the day
// as the source: a term gets one observation and one hit per day it passes
// the repeat threshold, so a replayed or concurrent run for the same day is
// a no-op however many documents named the term.
func (s *TermsAnalysisService) mergeNews(ctx context.Context, day string, kept []newsCandidate) (model.TermsCounters, error) {
	if len(kept) == 0 {
		return model.TermsCounters{}, nil
	}
	terms := make([]ExtractedTerm, 0, len(kept))
	for _, c := range kept {
		f := c.term
		terms = append(terms, ExtractedTerm{
			DisplayAs: f.DisplayAs, Phrase: f.Phrase, Type: f.Type, SourceTypes: f.SourceTypes,
			Confidence: f.Confidence, TranscribeKo: f.TranscribeKo, TranscribeEn: f.TranscribeEn,
		})
	}
	res, err := s.merger.MergeScoped(ctx, TermsScope{PK: model.PrefixDictCommon}, terms,
		TermsSourceRef{Kind: model.DictSourceNews, Ref: day, OwnerID: model.DictNewsActor})
	var total model.TermsCounters
	if res != nil {
		total = res.Counters
		if res.CleanupErr != nil {
			log.Printf("terms news: cleanup=failed")
		}
	}
	return total, err
}
