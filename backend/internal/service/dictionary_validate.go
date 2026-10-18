package service

import (
	_ "embed"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/ttobak/backend/internal/model"
)

// Shared dictionary term limits (manual and automatic terms, both scopes).
const (
	TermMinRunes       = 2
	TermMaxRunes       = 40
	TermMaxBytes       = 120
	TermMaxHeardAs     = 5
	TermPhraseMaxRunes = 256
	termTypeMaxRunes   = 32
)

// ErrInvalidDictionaryTerm is the sentinel behind every TermInputError.
var ErrInvalidDictionaryTerm = errors.New("invalid dictionary term")

// TermInputError names the rejected field; its message never echoes input.
type TermInputError struct {
	Field  string
	Reason string
}

func (e *TermInputError) Error() string { return e.Field + ": " + e.Reason }
func (e *TermInputError) Unwrap() error { return ErrInvalidDictionaryTerm }

func termInputError(field, reason string) error { return &TermInputError{Field: field, Reason: reason} }

//go:embed terms_blocklist.txt
var termsBlocklistSource string

var termsBlocklist = func() map[string]bool {
	set := map[string]bool{}
	for _, line := range strings.Split(termsBlocklistSource, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		set[NormalizeTermKey(line)] = true
	}
	return set
}()

var termTypePattern = regexp.MustCompile(`^[a-z][a-z_-]*$`)

// ValidatedTerm is the normalized result of ValidateTermInput.
type ValidatedTerm struct {
	DisplayAs string
	HeardAs   []string
	Type      string
	TermKey   string
}

// ValidateTermInput is the shared term validator (manual and automatic terms,
// both scopes): NFKC and trim, no Cc/Cf or line separators, displayAs 2-40
// runes and 120 bytes, at most five 2-40 rune aliases that are deduplicated,
// not blocklisted and not the term's own key.
func ValidateTermInput(displayAs string, heardAs []string, termType string) (ValidatedTerm, error) {
	display, err := validateTermText("displayAs", displayAs)
	if err != nil {
		return ValidatedTerm{}, err
	}
	key := TermKey(display)
	if key == "" {
		return ValidatedTerm{}, termInputError("displayAs", "must contain letters or digits")
	}
	heard, err := validateHeardAs(heardAs, NormalizeTermKey(display))
	if err != nil {
		return ValidatedTerm{}, err
	}
	typ, err := validateTermType(termType)
	if err != nil {
		return ValidatedTerm{}, err
	}
	return ValidatedTerm{DisplayAs: display, HeardAs: heard, Type: typ, TermKey: key}, nil
}

// normalizeTermText applies NFKC and trims surrounding space separators only,
// so edge tabs and line breaks still reach checkTermRunes and are rejected.
func normalizeTermText(s string) string { return trimTermSpaces(norm.NFKC.String(s)) }

func trimTermSpaces(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return unicode.Is(unicode.Zs, r) })
}

func checkTermRunes(field, s string) error {
	if !utf8.ValidString(s) {
		return termInputError(field, "must be valid UTF-8")
	}
	for _, r := range s {
		if unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
			return termInputError(field, "must not contain control, format, tab or line-break characters")
		}
	}
	return nil
}

func validateTermText(field, raw string) (string, error) {
	s := normalizeTermText(raw)
	if err := checkTermRunes(field, s); err != nil {
		return "", err
	}
	if n := utf8.RuneCountInString(s); n < TermMinRunes || n > TermMaxRunes || len(s) > TermMaxBytes {
		return "", termInputError(field, fmt.Sprintf("must be %d-%d characters and at most %d bytes", TermMinRunes, TermMaxRunes, TermMaxBytes))
	}
	return s, nil
}

// validateHeardAs normalizes aliases; empty entries are dropped.
func validateHeardAs(heardAs []string, selfKey string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, raw := range heardAs {
		if normalizeTermText(raw) == "" {
			continue
		}
		alias, err := validateTermText("heardAs", raw)
		if err != nil {
			return nil, err
		}
		key := NormalizeTermKey(alias)
		switch {
		case key == "":
			return nil, termInputError("heardAs", "must contain letters or digits")
		case key == selfKey:
			return nil, termInputError("heardAs", "must differ from the term itself")
		case termsBlocklist[key]:
			return nil, termInputError("heardAs", "is a common word that cannot be an alias")
		case seen[key]:
			continue
		}
		seen[key] = true
		out = append(out, alias)
	}
	if len(out) > TermMaxHeardAs {
		return nil, termInputError("heardAs", fmt.Sprintf("at most %d aliases", TermMaxHeardAs))
	}
	return out, nil
}

func validateTermType(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if utf8.RuneCountInString(raw) > termTypeMaxRunes || !termTypePattern.MatchString(raw) {
		return "", termInputError("type", "must be a short lowercase identifier")
	}
	return raw, nil
}

// validateTermPhrase checks the phrase as plain text; the AWS phrase grammar
// belongs to the vocabulary builder.
func validateTermPhrase(raw string) (string, error) {
	s := normalizeTermText(raw)
	if err := checkTermRunes("phrase", s); err != nil {
		return "", err
	}
	if utf8.RuneCountInString(s) > TermPhraseMaxRunes {
		return "", termInputError("phrase", fmt.Sprintf("at most %d characters", TermPhraseMaxRunes))
	}
	return s, nil
}

func validateAliasMode(raw string) (string, error) {
	switch raw {
	case "", model.TermAliasHint:
		return model.TermAliasHint, nil
	case model.TermAliasAlways:
		return model.TermAliasAlways, nil
	}
	return "", termInputError("aliasMode", "must be hint or always")
}

// splitLegacySoundsLike splits the legacy comma-separated soundsLike field.
func splitLegacySoundsLike(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = trimTermSpaces(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
