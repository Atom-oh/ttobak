package service

import (
	"context"
	"errors"
	"testing"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// partitionedDictStore routes each partition key to its own memDictStore, so
// a run that writes USER# and ACCOUNT# rows keeps them apart.
type partitionedDictStore map[string]*memDictStore

func (p partitionedDictStore) at(pk string) *memDictStore {
	if p[pk] == nil {
		p[pk] = newMemDictStore()
	}
	return p[pk]
}

func (p partitionedDictStore) ListDictTerms(ctx context.Context, pk string) ([]model.DictTerm, error) {
	return p.at(pk).ListDictTerms(ctx, pk)
}
func (p partitionedDictStore) GetDictTerm(ctx context.Context, pk, key string) (*model.DictTerm, error) {
	return p.at(pk).GetDictTerm(ctx, pk, key)
}
func (p partitionedDictStore) ListDictObs(ctx context.Context, pk, key string) ([]model.DictObservation, error) {
	return p.at(pk).ListDictObs(ctx, pk, key)
}
func (p partitionedDictStore) CreateAutoTermChecked(ctx context.Context, term *model.DictTerm, obs *model.DictObservation, member *repository.MembershipCheck) error {
	return p.at(term.PK).CreateAutoTermChecked(ctx, term, obs, member)
}
func (p partitionedDictStore) MergeAutoObservation(ctx context.Context, in repository.DictMergeInput) error {
	return p.at(in.PK).MergeAutoObservation(ctx, in)
}
func (p partitionedDictStore) PromoteSuggestedTerm(ctx context.Context, in repository.DictMergeInput, content repository.DictPromotion) error {
	return p.at(in.PK).PromoteSuggestedTerm(ctx, in, content)
}
func (p partitionedDictStore) EvictAutoTerm(ctx context.Context, pk, key string, v int64) (repository.DictWriteResult, error) {
	return p.at(pk).EvictAutoTerm(ctx, pk, key, v)
}
func (p partitionedDictStore) DeleteDictObs(ctx context.Context, pk, sk string) error {
	return p.at(pk).DeleteDictObs(ctx, pk, sk)
}

func accountScope(accountID, userID string) TermsScope {
	return TermsScope{PK: model.PrefixAccount + accountID, Member: &repository.MembershipCheck{AccountID: accountID, UserID: userID}}
}

func meetingSource(meetingID string) TermsSourceRef {
	return TermsSourceRef{Kind: model.DictSourceMeeting, Ref: meetingID, OwnerID: "owner"}
}

func TestMergeScopedAccountRequiresMember(t *testing.T) {
	bad := []struct {
		name  string
		scope TermsScope
		ref   TermsSourceRef
	}{
		{"account without a member", TermsScope{PK: "ACCOUNT#a1"}, meetingSource("m1")},
		{"member of another account", TermsScope{PK: "ACCOUNT#a1", Member: &repository.MembershipCheck{AccountID: "a2", UserID: "owner"}}, meetingSource("m1")},
		{"member is not the owner", accountScope("a1", "someone-else"), meetingSource("m1")},
		{"common scope", TermsScope{PK: model.PrefixDictCommon}, meetingSource("m1")},
		{"another user's personal scope", TermsScope{PK: "USER#someone-else"}, meetingSource("m1")},
		{"malformed partition", TermsScope{PK: "ACCOUNT#"}, meetingSource("m1")},
		{"non-meeting source", accountScope("a1", "owner"), TermsSourceRef{Kind: model.DictSourceNews, Ref: "n1", OwnerID: "owner"}},
		{"missing source ref", accountScope("a1", "owner"), TermsSourceRef{Kind: model.DictSourceMeeting, OwnerID: "owner"}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemDictStore()
			if _, err := newTestMerger(store).MergeScoped(context.Background(), tc.scope, []ExtractedTerm{cand("AlphaGrid")}, tc.ref); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("want ErrInvalidInput, got %v", err)
			}
			if len(store.terms) != 0 || store.calls["list"] != 0 {
				t.Fatalf("a rejected scope must not read or write: %v", store.calls)
			}
		})
	}

	t.Run("non-member is refused by the transaction check", func(t *testing.T) {
		store := newMemDictStore()
		store.members = map[string]bool{"a1/u2": true}
		res, err := newTestMerger(store).MergeScoped(context.Background(), accountScope("a1", "owner"), []ExtractedTerm{cand("AlphaGrid")}, meetingSource("m1"))
		if !errors.Is(err, repository.ErrNotAccountMember) || len(store.terms) != 0 {
			t.Fatalf("err=%v terms=%d", err, len(store.terms))
		}
		if len(res.Accepted) != 0 {
			t.Fatalf("a refused (cancelled) write must not be recorded as observed: %+v", res.Accepted)
		}
	})

	t.Run("member writes account rows with meeting provenance", func(t *testing.T) {
		store := newMemDictStore()
		store.members = map[string]bool{"a1/owner": true}
		m := newTestMerger(store)
		scope := accountScope("a1", "owner")
		res, err := m.MergeScoped(context.Background(), scope, []ExtractedTerm{cand("AlphaGrid", "알파그리드")}, meetingSource("m1"))
		if err != nil || res.Counters.Created != 1 {
			t.Fatalf("err=%v counters=%+v", err, res.Counters)
		}
		term, ok := store.term("AlphaGrid")
		if !ok || term.PK != "ACCOUNT#a1" || term.Scope != model.TermScopeAccount || term.State != model.TermStateActive || term.CreatedBy != "owner" {
			t.Fatalf("account term %+v", term)
		}
		if len(term.Sources) != 1 || term.Sources[0].Kind != model.DictSourceMeeting || term.Sources[0].Ref != "m1" || term.Sources[0].At == "" {
			t.Fatalf("sources %+v", term.Sources)
		}
		obs := store.obs[model.DictObsSK(term.TermKey, "m1")]
		if obs.PK != "ACCOUNT#a1" || obs.MeetingID != "m1" || obs.OwnerID != "owner" || obs.SourceKind != model.DictSourceMeeting || obs.SourceRef != "m1" {
			t.Fatalf("observation %+v", obs)
		}
		if store.lastMember == nil || *store.lastMember != *scope.Member {
			t.Fatalf("membership check %+v", store.lastMember)
		}

		store.lastMember = nil
		res, err = m.MergeScoped(context.Background(), scope, []ExtractedTerm{cand("AlphaGrid")}, meetingSource("m2"))
		if err != nil || res.Counters.Merged != 1 || store.lastMember == nil {
			t.Fatalf("second meeting: err=%v counters=%+v member=%v", err, res.Counters, store.lastMember)
		}
		term, _ = store.term("AlphaGrid")
		if term.Hits != 2 || len(term.Sources) != 2 || term.Sources[0].Ref != "m2" || term.Sources[1].Ref != "m1" {
			t.Fatalf("merge must reinforce and prepend the source: hits=%d sources=%+v", term.Hits, term.Sources)
		}
	})

	t.Run("tainted meeting only suggests", func(t *testing.T) {
		store := newMemDictStore()
		ref := meetingSource("m1")
		ref.Tainted = true
		res, err := newTestMerger(store).MergeScoped(context.Background(), accountScope("a1", "owner"), []ExtractedTerm{cand("AlphaGrid")}, ref)
		term, _ := store.term("AlphaGrid")
		if err != nil || res.Counters.Suggested != 1 || term.State != model.TermStateSuggested {
			t.Fatalf("err=%v counters=%+v state=%s", err, res.Counters, term.State)
		}
	})
}

// The personal wrapper keeps its contract and now records meeting provenance.
func TestMergeScopedPersonalWrapperRecordsSource(t *testing.T) {
	store := newMemDictStore()
	mustMerge(t, newTestMerger(store), ref("A"), cand("AlphaGrid"))
	term, _ := store.term("AlphaGrid")
	if term.Scope != model.TermScopeUser || len(term.Sources) != 1 || term.Sources[0].Kind != model.DictSourceMeeting || term.Sources[0].Ref != "A" {
		t.Fatalf("personal term %+v", term)
	}
	if obs := store.obs[model.DictObsSK(term.TermKey, "A")]; obs.SummaryGeneration != 1 || obs.SourceKind != model.DictSourceMeeting {
		t.Fatalf("observation %+v", obs)
	}
	if store.lastMember != nil {
		t.Fatal("a personal write carries no membership check")
	}
}
