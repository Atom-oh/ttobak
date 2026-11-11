package service

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Live translation glossary. Quality "high" segments carry the user's own
// dictionary terms that occur in the segment, so the interpreter keeps the
// user's spelling (and maps a listed mishearing to it). The terms come from
// GlossaryForMeeting without an Account (Personal, then Common, active rows,
// user-edited Common rows excluded), cached per user for a few minutes.
const (
	translateGlossaryTTL        = 5 * time.Minute
	translateGlossaryMaxUsers   = 256
	translateGlossaryMaxEntries = 20
	translateGlossaryMaxHeard   = 3
	translateGlossaryMinRunes   = 2
	// translateGlossaryReadTimeout bounds the optional dictionary read so a
	// slow or throttled table never spends the live caption's deadline
	// (the browser abandons a final translation after ten seconds).
	translateGlossaryReadTimeout = 800 * time.Millisecond
	// translateGlossaryFailureTTL caches a failed or timed-out read as an
	// empty glossary, so a struggling table is not retried on every segment.
	translateGlossaryFailureTTL = 30 * time.Second
)

// TranslateGlossarySource reads a user's glossary; *DictionaryService
// implements it.
type TranslateGlossarySource interface {
	GlossaryForMeeting(ctx context.Context, ownerID, accountID string) ([]GlossaryTerm, error)
}

var _ TranslateGlossarySource = (*DictionaryService)(nil)

// TranslateGlossaryEntry is one term sent to the interpreter.
type TranslateGlossaryEntry struct {
	Term  string   `json:"term"`
	Heard []string `json:"heard_as,omitempty"`
}

type translateGlossaryCache struct {
	source  TranslateGlossarySource
	now     func() time.Time
	timeout time.Duration
	mu      sync.Mutex
	users   map[string]translateGlossaryCacheEntry
}

type translateGlossaryCacheEntry struct {
	terms   []GlossaryTerm
	expires time.Time
}

// SetGlossarySource makes quality "high" segments carry the matching
// dictionary terms of the requesting user; nil disables the glossary.
func (s *TranslateService) SetGlossarySource(source TranslateGlossarySource) {
	if source == nil {
		s.glossary = nil
		return
	}
	s.glossary = &translateGlossaryCache{source: source, now: time.Now, timeout: translateGlossaryReadTimeout, users: map[string]translateGlossaryCacheEntry{}}
}

// terms returns the cached glossary of userID. The read has its own short
// deadline; a failed or timed-out read yields no glossary and is cached as
// empty for translateGlossaryFailureTTL. It never fails translation.
func (c *translateGlossaryCache) terms(ctx context.Context, userID string) []GlossaryTerm {
	now := c.now()
	c.mu.Lock()
	if entry, ok := c.users[userID]; ok && now.Before(entry.expires) {
		c.mu.Unlock()
		return entry.terms
	}
	c.mu.Unlock()
	readCtx, cancel := context.WithTimeout(ctx, c.timeout)
	terms, err := c.source.GlossaryForMeeting(readCtx, userID, "")
	cancel()
	ttl := translateGlossaryTTL
	if err != nil {
		reason := "error"
		if errors.Is(err, context.DeadlineExceeded) {
			reason = "timeout"
		}
		log.Printf("translate glossary: read %s for user %s", reason, userID)
		terms, ttl = nil, translateGlossaryFailureTTL
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.users) >= translateGlossaryMaxUsers {
		for key, entry := range c.users {
			if !now.Before(entry.expires) {
				delete(c.users, key)
			}
		}
		if len(c.users) >= translateGlossaryMaxUsers {
			c.users = map[string]translateGlossaryCacheEntry{}
		}
	}
	c.users[userID] = translateGlossaryCacheEntry{terms: terms, expires: now.Add(ttl)}
	return terms
}

// SelectTranslateGlossary keeps the terms whose display form or a heardAs
// spelling occurs in text, in glossary order, at most
// translateGlossaryMaxEntries. Latin forms match case-insensitively on word
// boundaries; other forms match as substrings (Korean particles attach).
func SelectTranslateGlossary(terms []GlossaryTerm, text string) []TranslateGlossaryEntry {
	if text == "" {
		return nil
	}
	lower := strings.ToLower(text)
	var out []TranslateGlossaryEntry
	seen := map[string]bool{}
	for _, term := range terms {
		if len(out) == translateGlossaryMaxEntries {
			break
		}
		if !promptSafeTerm(term.DisplayAs) || seen[term.DisplayAs] {
			continue
		}
		matched := glossaryFormIn(lower, term.DisplayAs)
		var heard []string
		for _, h := range term.HeardAs {
			if len(heard) == translateGlossaryMaxHeard {
				break
			}
			if h != term.DisplayAs && promptSafeTerm(h) && glossaryFormIn(lower, h) {
				heard = append(heard, h)
			}
		}
		if !matched && len(heard) == 0 {
			continue
		}
		seen[term.DisplayAs] = true
		out = append(out, TranslateGlossaryEntry{Term: term.DisplayAs, Heard: heard})
	}
	return out
}

// glossaryFormIn reports whether form occurs in the lowercased text.
func glossaryFormIn(lowerText, form string) bool {
	if utf8.RuneCountInString(form) < translateGlossaryMinRunes {
		return false
	}
	needle := strings.ToLower(form)
	latin := isLatinOnly(form)
	for from := 0; ; {
		at := strings.Index(lowerText[from:], needle)
		if at < 0 {
			return false
		}
		start := from + at
		end := start + len(needle)
		if !latin || (!wordRuneBefore(lowerText, start) && !wordRuneAfter(lowerText, end)) {
			return true
		}
		from = start + 1
	}
}

func wordRuneBefore(text string, at int) bool {
	if at == 0 {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(text[:at])
	return isLatinWordRune(r)
}

func wordRuneAfter(text string, at int) bool {
	if at >= len(text) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(text[at:])
	return isLatinWordRune(r)
}

// isLatinWordRune treats Latin letters and digits as word characters, so a
// Latin term next to a Korean particle ("SageMaker를") still matches.
func isLatinWordRune(r rune) bool {
	return unicode.IsDigit(r) || (unicode.IsLetter(r) && unicode.Is(unicode.Latin, r))
}
