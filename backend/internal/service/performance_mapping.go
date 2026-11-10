package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// PerformanceAccountCandidates lists the accounts the user can currently read
// with their names and aliases. AccountService implements it.
type PerformanceAccountCandidates interface {
	ListReadableAccountCandidates(ctx context.Context, userID string) ([]NamedCandidate, error)
}

var _ PerformanceAccountCandidates = (*AccountService)(nil)

// readablePerformanceAccounts is the set of account IDs the owner can read
// now, from the same discovery /missed uses. It fails closed when the account
// reader cannot list candidates.
func (svc *PerformanceService) readablePerformanceAccounts(ctx context.Context, userID string) (map[string]bool, error) {
	source, ok := svc.accounts.(PerformanceAccountCandidates)
	if !ok {
		return nil, ErrPerformanceSourcesUnavailable
	}
	candidates, err := source.ListReadableAccountCandidates(ctx, userID)
	if err != nil {
		return nil, err
	}
	readable := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		if candidate.ID != "" {
			readable[candidate.ID] = true
		}
	}
	return readable, nil
}

// ListReadableAccountCandidates returns the caller's readable accounts using
// the same discovery as alias resolution; it adds no access rule.
func (s *AccountService) ListReadableAccountCandidates(ctx context.Context, userID string) ([]NamedCandidate, error) {
	ids, err := listReadableAccountIDs(ctx, s.repo, userID, nil)
	if err != nil {
		return nil, err
	}
	candidates := make([]NamedCandidate, 0, len(ids))
	for _, id := range ids {
		account, err := s.repo.GetAccount(ctx, id)
		if err != nil {
			return nil, err
		}
		if account == nil {
			continue
		}
		candidates = append(candidates, NamedCandidate{Kind: "account", ID: id, Name: account.Name, Aliases: account.Aliases, Domains: account.Domains})
	}
	return candidates, nil
}

// MatchSFDCAccounts maps SFDC account ID to account ID for assignments whose
// folded name equals exactly one account's folded name or alias. Ambiguous and
// unmatched assignments are omitted; there is no fuzzy matching.
func MatchSFDCAccounts(assignments []model.SFDCAssignment, accounts []NamedCandidate) map[string]string {
	index := make(map[string]map[string]bool)
	add := func(label, id string) {
		folded := NormalizePersonName(label)
		if folded == "" {
			return
		}
		if index[folded] == nil {
			index[folded] = make(map[string]bool)
		}
		index[folded][id] = true
	}
	for _, account := range accounts {
		add(account.Name, account.ID)
		for _, alias := range account.Aliases {
			add(alias, account.ID)
		}
	}
	matched := make(map[string]string)
	for _, assignment := range assignments {
		ids := index[NormalizePersonName(assignment.Name)]
		if len(ids) != 1 {
			continue
		}
		for id := range ids {
			matched[assignment.SFDCAccountID] = id
		}
	}
	return matched
}

// autoMapAssignments writes unique matches for the owner's assignments. A
// manual mapping is kept by the store's onlyIfNotManual guard. It returns the
// joined failures without user data; the caller logs them.
func (svc *PerformanceService) autoMapAssignments(ctx context.Context, userID string) error {
	source, ok := svc.accounts.(PerformanceAccountCandidates)
	if !ok {
		return nil
	}
	assignments, err := svc.store.ListPerformanceAssignments(ctx, userID)
	if err != nil {
		return fmt.Errorf("list assignments: %w", err)
	}
	candidates, err := source.ListReadableAccountCandidates(ctx, userID)
	if err != nil {
		return fmt.Errorf("list readable accounts: %w", err)
	}
	matched := MatchSFDCAccounts(assignments, candidates)
	existing, err := svc.store.ListAccountMappings(ctx, userID)
	if err != nil {
		return fmt.Errorf("list mappings: %w", err)
	}
	current := make(map[string]model.AccountMapping, len(existing))
	for _, mapping := range existing {
		current[mapping.SFDCAccountID] = mapping
	}
	var failures []error
	var stale []string
	for sfdcID, mapping := range current {
		if _, ok := matched[sfdcID]; !ok && !mapping.Manual {
			stale = append(stale, sfdcID)
		}
	}
	sort.Strings(stale)
	for _, sfdcID := range stale {
		if err := svc.store.DeleteAccountMappingIfAutomatic(ctx, userID, sfdcID); err != nil {
			failures = append(failures, err)
		}
	}
	sfdcIDs := make([]string, 0, len(matched))
	for sfdcID := range matched {
		if prior, found := current[sfdcID]; found && (prior.Manual || prior.AccountID == matched[sfdcID]) {
			continue
		}
		sfdcIDs = append(sfdcIDs, sfdcID)
	}
	sort.Strings(sfdcIDs)
	updatedAt := svc.now().UTC().Format(time.RFC3339)
	for _, sfdcID := range sfdcIDs {
		mapping := model.AccountMapping{SFDCAccountID: sfdcID, AccountID: matched[sfdcID], UpdatedAt: updatedAt}
		err := svc.store.PutAccountMapping(ctx, userID, mapping, true)
		if err != nil && !errors.Is(err, repository.ErrConditionFailed) {
			failures = append(failures, err)
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%d mapping writes failed: %w", len(failures), errors.Join(failures...))
	}
	return nil
}
