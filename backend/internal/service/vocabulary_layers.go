package service

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// accountOrderMeetingWindow is how many of the user's newest meetings rank
// Accounts for the vocabulary plan.
const accountOrderMeetingWindow = 200

// SetAccountOrder sets the source of the user's member Account IDs in
// recency order (AccountRecencyOrder in the API and summarize Lambdas).
// Unset, the vocabulary plan has no Account layer.
func (s *DictionaryService) SetAccountOrder(fn func(ctx context.Context, userID string) ([]string, error)) {
	s.accountOrder = fn
}

// AccountRecencySource is the repository surface AccountRecencyOrder reads.
type AccountRecencySource interface {
	ListMeetings(ctx context.Context, params repository.ListMeetingsParams) (*repository.ListMeetingsResult, error)
	ListAccountsForUser(ctx context.Context, userID string) ([]model.AccountMember, error)
}

// AccountRecencyOrder orders the user's current member Accounts by the
// user's newest meetings (the newest accountOrderMeetingWindow), then the
// remaining memberships in ListAccountsForUser order. A meeting linked to an
// Account the user is no longer a member of never adds it.
func AccountRecencyOrder(src AccountRecencySource) func(ctx context.Context, userID string) ([]string, error) {
	return func(ctx context.Context, userID string) ([]string, error) {
		members, err := src.ListAccountsForUser(ctx, userID)
		if err != nil {
			return nil, fmt.Errorf("list accounts for user: %w", err)
		}
		if len(members) == 0 {
			return nil, nil
		}
		page, err := src.ListMeetings(ctx, repository.ListMeetingsParams{UserID: userID, Limit: accountOrderMeetingWindow})
		if err != nil {
			return nil, fmt.Errorf("list meetings: %w", err)
		}
		member := make(map[string]bool, len(members))
		for _, m := range members {
			member[m.AccountID] = true
		}
		var out []string
		taken := map[string]bool{}
		add := func(id string) {
			if id != "" && member[id] && !taken[id] {
				taken[id] = true
				out = append(out, id)
			}
		}
		if page != nil {
			for _, m := range page.Meetings {
				add(m.AccountID)
			}
		}
		for _, m := range members {
			add(m.AccountID)
		}
		return out, nil
	}
}

// vocabularyLayers lists the plan's layers: Personal manual, the user's
// Accounts in recency order, Personal auto, Common.
func (s *DictionaryService) vocabularyLayers(ctx context.Context, userID string) ([]vocabLayer, error) {
	return s.listVocabularyLayers(ctx, userID, s.vocabularyAccountOrder(ctx, userID))
}

// vocabularyAccountOrder reads the Account order; a failure skips the
// Account layer (logged) instead of failing the build.
func (s *DictionaryService) vocabularyAccountOrder(ctx context.Context, userID string) []string {
	if s.accountOrder == nil || s.isMember == nil {
		return nil
	}
	ids, err := s.accountOrder(ctx, userID)
	if err != nil {
		log.Printf("vocabulary account layer skipped for user %s: %v", userID, err)
		return nil
	}
	return ids
}

// listVocabularyLayers reads every layer's rows. Each Account's membership is
// re-checked here (the order may be stale), so an Account the user left
// contributes nothing; a list or membership read error fails the listing.
func (s *DictionaryService) listVocabularyLayers(ctx context.Context, userID string, accountIDs []string) ([]vocabLayer, error) {
	personal, err := s.repo.ListDictTerms(ctx, model.PrefixUser+userID)
	if err != nil {
		return nil, err
	}
	var manual, auto []model.DictTerm
	for _, row := range personal {
		if row.Origin == model.TermOriginManual {
			manual = append(manual, row)
		} else {
			auto = append(auto, row)
		}
	}
	layers := []vocabLayer{{name: "personal-manual", rows: manual}}
	seen := map[string]bool{}
	for _, id := range accountIDs {
		if id == "" || seen[id] || strings.ContainsAny(id, "#/") || s.isMember == nil {
			continue
		}
		seen[id] = true
		ok, err := s.isMember(ctx, id, userID)
		if err != nil {
			return nil, fmt.Errorf("check account membership: %w", err)
		}
		if !ok {
			continue
		}
		rows, err := s.repo.ListDictTerms(ctx, model.PrefixAccount+id)
		if err != nil {
			return nil, err
		}
		layers = append(layers, vocabLayer{name: model.DictLayerAccountPrefix + id, rows: rows})
	}
	common, err := s.repo.ListDictTerms(ctx, model.PrefixDictCommon)
	if err != nil {
		return nil, err
	}
	return append(layers, vocabLayer{name: "personal-auto", rows: auto}, vocabLayer{name: "common", rows: common}), nil
}
