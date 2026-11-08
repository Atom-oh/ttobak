package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/ttobak/backend/internal/fanout"
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
	ListRecentMeetingAccountIDs(ctx context.Context, userID string, limit int) ([]string, error)
	ListAccountsForUser(ctx context.Context, userID string) ([]model.AccountMember, error)
}

// AccountRecencyOrder orders the user's current member Accounts by the
// user's newest meetings (the newest accountOrderMeetingWindow, read with a
// projection-only query), then the
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
		recent, err := src.ListRecentMeetingAccountIDs(ctx, userID, accountOrderMeetingWindow)
		if err != nil {
			return nil, fmt.Errorf("list recent meeting accounts: %w", err)
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
		for _, id := range recent {
			add(id)
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
	ids, err := s.vocabularyAccountOrder(ctx, userID)
	if err != nil {
		return nil, err
	}
	return s.listVocabularyLayers(ctx, userID, ids, s.newMembershipMemo(userID))
}

// vocabularyAccountOrder reads the Account order. Without an order func or a
// membership reader there is no Account layer (nil, no error); an order
// failure is an error, so the caller skips the step rather than building a
// table without the Account terms.
func (s *DictionaryService) vocabularyAccountOrder(ctx context.Context, userID string) ([]string, error) {
	if s.accountOrder == nil || s.isMember == nil {
		return nil, nil
	}
	ids, err := s.accountOrder(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("read account order: %w", err)
	}
	return ids, nil
}

// SetMembershipBatchReader sets the consistent batched form of the membership
// reader for the vocabulary plan: fn returns the member subset of accountIDs.
// It only replaces per-Account reads; without SetMembershipReader there is
// still no Account layer, and unset the plan checks each Account itself.
func (s *DictionaryService) SetMembershipBatchReader(fn func(ctx context.Context, accountIDs []string, userID string) (map[string]bool, error)) {
	s.isMemberBatch = fn
}

// BatchMembershipReader is the batched form of the GetMember-based reader:
// the same ACCOUNT#{id}/MEMBER#{user} rows with consistent reads. A failed
// read fails the whole call; no partial result is returned.
func BatchMembershipReader(repo accountRowBatchReader) func(ctx context.Context, accountIDs []string, userID string) (map[string]bool, error) {
	return func(ctx context.Context, accountIDs []string, userID string) (map[string]bool, error) {
		out := make(map[string]bool, len(accountIDs))
		for start := 0; start < len(accountIDs); start += accountPrimeCallKeys {
			chunk := accountIDs[start:min(start+accountPrimeCallKeys, len(accountIDs))]
			keys := make([]repository.AccountRowKey, len(chunk))
			for i, id := range chunk {
				keys[i] = repository.AccountRowKey{AccountID: id, UserID: userID}
			}
			rows, err := repo.BatchGetAccountRows(ctx, keys)
			if err != nil {
				return nil, err
			}
			if rows == nil {
				return nil, ErrReadingData
			}
			for _, key := range keys {
				out[key.AccountID] = rows.Members[key] != nil
			}
		}
		return out, nil
	}
}

// membershipMemo remembers one call's successful Account membership reads so
// a multi-step ensure checks each Account once. Errors are never stored, and
// a memo is created per call, never shared across requests or users. It is
// used from one goroutine at a time.
type membershipMemo struct {
	s      *DictionaryService
	userID string
	known  map[string]bool
}

func (s *DictionaryService) newMembershipMemo(userID string) *membershipMemo {
	return &membershipMemo{s: s, userID: userID, known: map[string]bool{}}
}

// check reports membership for each of ids, reading only unknown ones, in one
// batch or concurrently. On a read error, failAt is the index of the first
// Account a sequential loop would have failed on and only results below it
// are returned; without an error failAt is len(ids).
func (m *membershipMemo) check(ctx context.Context, ids []string) (members []bool, failAt int, err error) {
	var unknown []int
	for i, id := range ids {
		if _, ok := m.known[id]; !ok {
			unknown = append(unknown, i)
		}
	}
	failAt = len(ids)
	switch {
	case len(unknown) == 0:
	case m.s.isMemberBatch != nil:
		want := make([]string, len(unknown))
		for k, i := range unknown {
			want[k] = ids[i]
		}
		got, batchErr := m.s.isMemberBatch(ctx, want, m.userID)
		if batchErr != nil {
			failAt, err = unknown[0], batchErr
			break
		}
		for _, id := range want {
			m.known[id] = got[id]
		}
	default:
		ok, errs := make([]bool, len(unknown)), make([]error, len(unknown))
		stop := fanout.Ordered(ctx, len(unknown), listReadConcurrency, func(ctx context.Context, k int) error {
			ok[k], errs[k] = m.s.isMember(ctx, ids[unknown[k]], m.userID)
			return errs[k]
		})
		for k := 0; k < stop; k++ {
			m.known[ids[unknown[k]]] = ok[k]
		}
		if stop < len(unknown) {
			failAt, err = unknown[stop], errs[stop]
		}
	}
	members = make([]bool, failAt)
	for i := range members {
		members[i] = m.known[ids[i]]
	}
	return members, failAt, err
}

// listVocabularyLayers reads every layer's rows. Each Account's membership is
// checked (memoized per ensure call, since the order may be stale) before its
// rows are read, so an Account the user left contributes nothing. A list or
// membership read error fails the listing with the error a sequential
// Personal, Account-by-Account (membership, then rows), Common loop would
// have returned. The Personal list runs alongside the membership checks,
// then the member Accounts' and Common's lists run concurrently.
func (s *DictionaryService) listVocabularyLayers(ctx context.Context, userID string, accountIDs []string, memo *membershipMemo) ([]vocabLayer, error) {
	var ids []string
	seen := map[string]bool{}
	for _, id := range accountIDs {
		if id == "" || seen[id] || strings.ContainsAny(id, "#/") || s.isMember == nil {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	var (
		personal    []model.DictTerm
		personalErr error
		members     []bool
		failAt      int
		memberErr   error
	)
	if stop := fanout.Ordered(ctx, 2, 2, func(ctx context.Context, k int) error {
		if k == 0 {
			personal, personalErr = s.repo.ListDictTerms(ctx, model.PrefixUser+userID)
			return personalErr
		}
		members, failAt, memberErr = memo.check(ctx, ids)
		return memberErr
	}); stop == 0 {
		return nil, personalErr
	}
	// Every member Account before the first membership failure is listed,
	// then Common unless a membership read failed (the listing fails anyway).
	var listed []int
	for i := 0; i < failAt; i++ {
		if members[i] {
			listed = append(listed, i)
		}
	}
	lists := len(listed)
	if memberErr == nil {
		lists++
	}
	rows, errs := make([][]model.DictTerm, lists), make([]error, lists)
	if stop := fanout.Ordered(ctx, lists, listReadConcurrency, func(ctx context.Context, k int) error {
		pk := model.PrefixDictCommon
		if k < len(listed) {
			pk = model.PrefixAccount + ids[listed[k]]
		}
		rows[k], errs[k] = s.repo.ListDictTerms(ctx, pk)
		return errs[k]
	}); stop < lists {
		return nil, errs[stop]
	}
	if memberErr != nil {
		return nil, fmt.Errorf("check account membership: %w", memberErr)
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
	for k, i := range listed {
		layers = append(layers, vocabLayer{name: model.DictLayerAccountPrefix + ids[i], rows: rows[k]})
	}
	return append(layers, vocabLayer{name: "personal-auto", rows: auto}, vocabLayer{name: "common", rows: rows[len(listed)]}), nil
}
