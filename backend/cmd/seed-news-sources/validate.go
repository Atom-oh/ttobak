package main

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ttobak/backend/internal/repository"
)

// These mirror the crawler's checks (news_discovery.valid_source_id and
// source_snapshot, news_discovery.search_domains): a definition they would
// reject fails the whole source run as INVALID_CONFIG, so it must not be seeded.
var (
	seedSourceID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	domainLabel  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

const (
	maxTerms         = 32
	maxTermRunes     = 200
	maxSearchDomains = 100
)

func validTerm(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || utf8.RuneCountInString(value) > maxTermRunes ||
		strings.ContainsRune(value, '"') {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validDomain(value string) bool {
	labels := strings.Split(value, ".")
	if len(value) < 3 || len(value) > 253 || len(labels) < 2 {
		return false
	}
	if strings.Trim(labels[len(labels)-1], "0123456789") == "" {
		return false
	}
	for _, label := range labels {
		if !domainLabel.MatchString(label) {
			return false
		}
	}
	return true
}

func uniqueValid(values []string, limit int, valid func(string) bool) error {
	if len(values) > limit {
		return fmt.Errorf("%d values exceed %d", len(values), limit)
	}
	seen := map[string]bool{}
	for _, value := range values {
		if !valid(value) || seen[value] {
			return fmt.Errorf("invalid or duplicate value %q", value)
		}
		seen[value] = true
	}
	return nil
}

func validateSources(sources []repository.OperatorNewsSource) error {
	ids := map[string]bool{}
	for _, src := range sources {
		if !seedSourceID.MatchString(src.SourceID) || strings.HasPrefix(src.SourceID, "__") || ids[src.SourceID] {
			return fmt.Errorf("source ID %q", src.SourceID)
		}
		ids[src.SourceID] = true
		if !validTerm(src.SourceName) {
			return fmt.Errorf("%s: source name", src.SourceID)
		}
		if err := uniqueValid(src.Aliases, maxTerms-1, validTerm); err != nil {
			return fmt.Errorf("%s: aliases: %w", src.SourceID, err)
		}
		if err := uniqueValid(src.NewsQueries, maxTerms, validTerm); err != nil {
			return fmt.Errorf("%s: news queries: %w", src.SourceID, err)
		}
		if err := uniqueValid(src.SearchDomains, maxSearchDomains, validDomain); err != nil {
			return fmt.Errorf("%s: search domains: %w", src.SourceID, err)
		}
	}
	return nil
}
