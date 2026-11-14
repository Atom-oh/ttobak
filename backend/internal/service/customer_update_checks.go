package service

import (
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/ttobak/backend/internal/model"
)

// Deterministic customer update draft checks: a port of
// backend/python/customer-update-agent/checks.py, pinned by
// contracts/customer-updates/draft-checks-v1.json. Python indexes by code
// point, so the scanners below work on []rune.

// ErrUpdateDraftEncoding means a check input string is not valid UTF-8.
var ErrUpdateDraftEncoding = errors.New("update draft input is not valid UTF-8")

// UpdateDraftCheckInput is one draft and the lists it is checked against.
type UpdateDraftCheckInput struct {
	Body        string
	Links       []string
	AllowedURLs []string
	BlockList   []string
	Stages      []string
}

// updateDraftCheckCodes is the fixed order of failure codes.
var updateDraftCheckCodes = []string{
	"empty",
	"too_long",
	"link_not_allowed",
	"link_internal",
	"link_missing",
	"blocked_name",
	"stage_word",
	"opportunity_word",
	"figure",
	"slack_control",
	"no_meeting_offer",
}

const updateDraftMaxChars = 2000

// Mirrors of contracts/customer-updates/draft-terms-ko-v1.json (tests compare).
var (
	updateOpportunityWords  = []string{"oppty", "opportunity", "오퍼튜니티", "기회 단계"}
	updateMeetingOfferWords = []string{"미팅", "세션", "자리", "통화", "시간"}
	updateFigureUnits       = []string{"원", "달러", "USD", "KRW", "%"}
)

// updateBareHostTLDs mirrors bareHostTlds in draft-checks-v1.json (tests compare).
var updateBareHostTLDs = []string{
	"com", "net", "org", "io", "co", "kr", "ai", "dev", "app", "me", "info", "biz", "xyz", "ly",
	"to", "cc", "gg", "tv", "us", "jp", "cn", "uk", "de", "fr", "link", "site", "online", "cloud", "tech",
	"ru", "sh", "top", "do", "tk", "zip", "mov", "land", "page", "run", "sbs", "icu", "vip", "work",
	"click", "live", "shop", "store", "blog", "news", "wiki", "edu", "gov", "in", "br", "au", "ca",
	"es", "it", "nl", "se", "ch", "pl", "tw", "hk", "sg", "id", "vn", "th", "my", "ph",
}

const (
	updateCurrencySigns = "$₩€¥£"
	// Whitespace is exactly these characters (never unicode.IsSpace/TrimSpace).
	updateWhitespace = " \t\n\v\f\r\u00a0\u3000"
	updateURLEnd     = "<>\"'"
	updateURLTrail   = ".,;:!?)"
)

var updateKoreanNumberUnits = []string{"만", "억", "천", "조"}

// ValidateUpdateDraftEncoding returns ErrUpdateDraftEncoding when the body or
// any list entry is not valid UTF-8. Call it before CheckUpdateDraft.
func ValidateUpdateDraftEncoding(in UpdateDraftCheckInput) error {
	if !utf8.ValidString(in.Body) {
		return ErrUpdateDraftEncoding
	}
	for _, list := range [][]string{in.Links, in.AllowedURLs, in.BlockList, in.Stages} {
		for _, s := range list {
			if !utf8.ValidString(s) {
				return ErrUpdateDraftEncoding
			}
		}
	}
	return nil
}

// CheckUpdateDraft returns failure codes in fixed order, each at most once;
// an empty (non-nil) slice means the draft passes. It assumes every input
// string is valid UTF-8 (see ValidateUpdateDraftEncoding). Pure: no I/O.
func CheckUpdateDraft(in UpdateDraftCheckInput) []string {
	found := map[string]bool{}
	body := []rune(in.Body)
	// empty: nothing left after trimming the whitespace set from both ends.
	if strings.Trim(in.Body, updateWhitespace) == "" {
		found["empty"] = true
	}
	// too_long: more than 2,000 Unicode code points.
	if utf8.RuneCountInString(in.Body) > updateDraftMaxChars {
		found["too_long"] = true
	}
	// link_missing: the links list is empty.
	if len(in.Links) == 0 {
		found["link_missing"] = true
	}
	allowed := make(map[string]bool, len(in.AllowedURLs))
	for _, u := range in.AllowedURLs {
		allowed[u] = true
	}
	linked := make(map[string]bool, len(in.Links))
	for _, u := range in.Links {
		linked[u] = true
	}
	// A links entry must be exactly an https:// URL in AllowedURLs.
	for _, u := range in.Links {
		if !strings.HasPrefix(u, "https://") || !allowed[u] {
			found["link_not_allowed"] = true
		}
	}
	// A body candidate must be exactly an https:// URL in AllowedURLs AND in Links.
	spans := updateCandidateSpans(body)
	candidates := make([]string, 0, len(spans))
	for _, s := range spans {
		u := string(body[s[0]:s[1]])
		candidates = append(candidates, u)
		if !strings.HasPrefix(u, "https://") || !allowed[u] || !linked[u] {
			found["link_not_allowed"] = true
		}
	}
	// link_internal: any links entry or body candidate whose host is internal.
	for _, u := range append(append([]string(nil), in.Links...), candidates...) {
		if model.IsUpdateInternalHost(updateURLHost(u)) {
			found["link_internal"] = true
		}
	}
	if updateContainsAny(in.Body, in.BlockList) {
		found["blocked_name"] = true
	}
	if updateContainsAny(in.Body, in.Stages) {
		found["stage_word"] = true
	}
	if updateContainsAny(in.Body, updateOpportunityWords) {
		found["opportunity_word"] = true
	}
	// figure: scanned with link candidate spans replaced by spaces, plus a
	// figure that straddles the end of a candidate span.
	blanked := []rune(updateASCIILower(in.Body))
	straddles := updateFigureStraddlesSpan(blanked, spans)
	for _, s := range spans {
		for k := s[0]; k < s[1]; k++ {
			blanked[k] = ' '
		}
	}
	if straddles || hasUpdateFigure(blanked) {
		found["figure"] = true
	}
	// slack_control: "<!" (here/channel/everyone/subteam) or "<@" mention.
	if strings.Contains(in.Body, "<!") || strings.Contains(in.Body, "<@") {
		found["slack_control"] = true
	}
	// no_meeting_offer: no meeting offer word as an exact substring.
	offer := false
	for _, w := range updateMeetingOfferWords {
		if strings.Contains(in.Body, w) {
			offer = true
			break
		}
	}
	if !offer {
		found["no_meeting_offer"] = true
	}
	out := []string{}
	for _, code := range updateDraftCheckCodes {
		if found[code] {
			out = append(out, code)
		}
	}
	return out
}

// EscapeUpdateSlack escapes & first, then < and >, for posting through the Slack API.
func EscapeUpdateSlack(text string) string {
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	return strings.ReplaceAll(text, ">", "&gt;")
}

// updateASCIILower lowercases only ASCII A-Z; every other byte is unchanged.
func updateASCIILower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

// updateContainsAny is an ASCII-case-insensitive substring test; empty needles are ignored.
func updateContainsAny(haystack string, needles []string) bool {
	h := updateASCIILower(haystack)
	for _, n := range needles {
		if n != "" && strings.Contains(h, updateASCIILower(n)) {
			return true
		}
	}
	return false
}

func isUpdateDigit(r rune) bool { return '0' <= r && r <= '9' }

func isUpdateHostRunRune(r rune) bool {
	return isASCIILetter(r) || isUpdateDigit(r) || r == '-' || r == '.'
}

// updateRunesHavePrefixAt reports whether r[j:] starts with s.
func updateRunesHavePrefixAt(r []rune, j int, s string) bool {
	for _, c := range s {
		if j >= len(r) || r[j] != c {
			return false
		}
		j++
	}
	return true
}

// updateCandidateEnd: a candidate ends at the first whitespace-set rune or one of <>"' (or end of body).
func updateCandidateEnd(body []rune, start int) int {
	e := start
	for e < len(body) && !strings.ContainsRune(updateWhitespace, body[e]) && !strings.ContainsRune(updateURLEnd, body[e]) {
		e++
	}
	return e
}

// updateTrimTrail is Python's rstrip(chars) on r[start:end]; it returns the new end.
func updateTrimTrail(r []rune, start, end int, chars string) int {
	for end > start && strings.ContainsRune(chars, r[end-1]) {
		end--
	}
	return end
}

// updateCandidateSpans returns [start,end) rune spans of link candidates, left to right.
func updateCandidateSpans(body []rune) [][2]int {
	var out [][2]int
	n := len(body)
	i := 0
	for i < n {
		c := body[i]
		// At the start of an ASCII letter run: "<2-10 letters>://" or "mailto:" (case-insensitive).
		if isASCIILetter(c) && (i == 0 || !isASCIILetter(body[i-1])) {
			j := i
			for j < n && isASCIILetter(body[j]) {
				j++
			}
			if (j-i >= 2 && j-i <= 10 && updateRunesHavePrefixAt(body, j, "://")) ||
				(updateASCIILower(string(body[i:j])) == "mailto" && updateRunesHavePrefixAt(body, j, ":")) {
				e := updateCandidateEnd(body, i)
				out = append(out, [2]int{i, updateTrimTrail(body, i, e, updateURLTrail)})
				i = e
				continue
			}
		}
		// Bare host: a maximal run of ASCII letters, digits, "-" and ".".
		if isUpdateHostRunRune(c) && (i == 0 || !isUpdateHostRunRune(body[i-1])) {
			r := i
			for r < n && isUpdateHostRunRune(body[r]) {
				r++
			}
			var prev rune
			if i > 0 {
				prev = body[i-1]
			}
			// Not after "@", and not after "/" unless that "/" is the second of "//".
			afterDoubleSlash := prev == '/' && i >= 2 && body[i-2] == '/'
			blocked := prev == '@' || (prev == '/' && !afterDoubleSlash)
			if !blocked {
				var end int
				var isLink bool
				if updateRunesHavePrefixAt(body, r, "/") {
					// An optional /path extends the candidate; the host part is the run.
					hostRun := string(body[i:r])
					end = updateTrimTrail(body, i, updateCandidateEnd(body, r), updateURLTrail)
					isLink = isUpdateBareHost(hostRun) || isUpdateDottedHost(hostRun)
				} else {
					// Without a path: trailing dots are dropped; a listed TLD, "www.", IPv4, or a dotted host after "//".
					end = updateTrimTrail(body, i, r, ".")
					hostRun := string(body[i:end])
					isLink = isUpdateBareHost(hostRun) || isUpdateIPv4(hostRun) ||
						(afterDoubleSlash && isUpdateDottedHost(hostRun))
				}
				if isLink {
					out = append(out, [2]int{i, end})
				}
			}
			i = r
			continue
		}
		i++
	}
	return out
}

// isUpdateBareHost: contains a dot and its last label is a listed TLD, or starts with "www." plus a label.
func isUpdateBareHost(run string) bool {
	if !strings.Contains(run, ".") {
		return false
	}
	low := updateASCIILower(run)
	if strings.HasPrefix(low, "www.") && len(low) > 4 {
		return true
	}
	last := low[strings.LastIndex(low, ".")+1:]
	for _, t := range updateBareHostTLDs {
		if last == t {
			return true
		}
	}
	return false
}

// isUpdateIPv4: exactly four dot-separated groups of 1-3 ASCII digits.
func isUpdateIPv4(run string) bool {
	groups := strings.Split(run, ".")
	if len(groups) != 4 {
		return false
	}
	for _, g := range groups {
		if len(g) < 1 || len(g) > 3 {
			return false
		}
		for _, ch := range g {
			if !isUpdateDigit(ch) {
				return false
			}
		}
	}
	return true
}

// isUpdateDottedHost: non-empty labels, at least one dot, a last label of at least 2 ASCII letters; or IPv4.
func isUpdateDottedHost(run string) bool {
	if isUpdateIPv4(run) {
		return true
	}
	labels := strings.Split(run, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if l == "" {
			return false
		}
	}
	last := labels[len(labels)-1]
	if len(last) < 2 {
		return false
	}
	for _, ch := range last {
		if !isASCIILetter(ch) {
			return false
		}
	}
	return true
}

// updateURLHost drops a "<2-10 letters>://" prefix; cuts at the first / ? #; keeps text after
// the last "@"; cuts at the first ":"; ASCII-lowercases; strips one trailing ".".
func updateURLHost(u string) string {
	rest := u
	if k := strings.Index(u, "://"); k >= 2 && k <= 10 && allUpdateLetters(u[:k]) {
		rest = u[k+3:]
	}
	for _, sep := range []string{"/", "?", "#"} {
		if k := strings.Index(rest, sep); k >= 0 {
			rest = rest[:k]
		}
	}
	if k := strings.LastIndex(rest, "@"); k >= 0 {
		rest = rest[k+1:]
	}
	if k := strings.Index(rest, ":"); k >= 0 {
		rest = rest[:k]
	}
	return strings.TrimSuffix(updateASCIILower(rest), ".")
}

func allUpdateLetters(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isASCIILetter(rune(s[i])) {
			return false
		}
	}
	return true
}

// hasUpdateFigure: a currency sign, one optional space, then an ASCII digit; or an ASCII digit,
// one optional space, then a figure unit or Korean number unit. text must already be ASCII-lowercased.
func hasUpdateFigure(text []rune) bool {
	units := updateFigureUnitList()
	n := len(text)
	for i, ch := range text {
		switch {
		case strings.ContainsRune(updateCurrencySigns, ch):
			j := i + 1
			if j < n && text[j] == ' ' {
				j++
			}
			if j < n && isUpdateDigit(text[j]) {
				return true
			}
		case isUpdateDigit(ch):
			j := i + 1
			if j < n && text[j] == ' ' {
				j++
			}
			for _, u := range units {
				if updateRunesHavePrefixAt(text, j, u) {
					return true
				}
			}
		}
	}
	return false
}

// updateFigureUnitList is the figure units (ASCII-lowercased), then the Korean number units.
func updateFigureUnitList() []string {
	units := make([]string, 0, len(updateFigureUnits)+len(updateKoreanNumberUnits))
	for _, u := range updateFigureUnits {
		units = append(units, updateASCIILower(u))
	}
	return append(units, updateKoreanNumberUnits...)
}

// updateFigureStraddlesSpan: a candidate span whose last rune is an ASCII digit, followed (after one
// optional whitespace-set rune) by a figure unit or Korean number unit. text must be ASCII-lowercased.
func updateFigureStraddlesSpan(text []rune, spans [][2]int) bool {
	units := updateFigureUnitList()
	for _, s := range spans {
		if s[1] <= s[0] || !isUpdateDigit(text[s[1]-1]) {
			continue
		}
		j := s[1]
		if j < len(text) && strings.ContainsRune(updateWhitespace, text[j]) {
			j++
		}
		for _, u := range units {
			if updateRunesHavePrefixAt(text, j, u) {
				return true
			}
		}
	}
	return false
}
