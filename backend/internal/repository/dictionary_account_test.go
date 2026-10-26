package repository

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

const acctKey = "0123456789abcdef0123456789abcdef"

// acctWrite is one write path under test; pk is the partition it targets.
type acctWrite struct {
	name string
	run  func(r *DictionaryRepository, pk string, member *MembershipCheck) error
	// transactional is true when the personal/common form is already a transaction.
	transactional bool
}

func acctTerm(pk string) (*model.DictTerm, *model.DictObservation) {
	term := &model.DictTerm{PK: pk, SK: model.DictTermSK(acctKey), TermKey: acctKey, State: model.TermStateActive, Origin: model.TermOriginAuto, DisplayAs: "AlphaGrid", Hits: 1, Version: 1}
	obs := &model.DictObservation{PK: pk, SK: model.DictObsSK(acctKey, "m1"), TermKey: acctKey, MeetingID: "m1", OwnerID: "u1"}
	return term, obs
}

func acctWrites() []acctWrite {
	ctx := context.Background()
	return []acctWrite{
		{"CreateAutoTermChecked", func(r *DictionaryRepository, pk string, m *MembershipCheck) error {
			term, obs := acctTerm(pk)
			return r.CreateAutoTermChecked(ctx, term, obs, m)
		}, true},
		{"MergeAutoObservation", func(r *DictionaryRepository, pk string, m *MembershipCheck) error {
			_, obs := acctTerm(pk)
			return r.MergeAutoObservation(ctx, DictMergeInput{PK: pk, TermKey: acctKey, Observation: obs, ExpectedVersion: 1, Member: m})
		}, true},
		{"PromoteSuggestedTerm", func(r *DictionaryRepository, pk string, m *MembershipCheck) error {
			_, obs := acctTerm(pk)
			in := DictMergeInput{PK: pk, TermKey: acctKey, Observation: obs, ExpectedVersion: 1, SourceTypes: []string{"meeting"}, Member: m}
			return r.PromoteSuggestedTerm(ctx, in, DictPromotion{DisplayAs: "AlphaGrid", FirstSourceMeetingID: "m1"})
		}, true},
		{"CreateDictTerm", func(r *DictionaryRepository, pk string, m *MembershipCheck) error {
			term, _ := acctTerm(pk)
			term.Origin = model.TermOriginManual
			return r.CreateDictTerm(ctx, term, m)
		}, false},
		{"UpdateDictTerm", func(r *DictionaryRepository, pk string, m *MembershipCheck) error {
			phrase := "alpha grid"
			return r.UpdateDictTerm(ctx, pk, acctKey, DictTermChange{Phrase: &phrase}, "u1", "now", 1, m)
		}, false},
		{"TombstoneDictTerm", func(r *DictionaryRepository, pk string, m *MembershipCheck) error {
			_, err := r.TombstoneDictTerm(ctx, pk, acctKey, "u1", "now", 1, m)
			return err
		}, false},
		{"RestoreDictTerm", func(r *DictionaryRepository, pk string, m *MembershipCheck) error {
			term, _ := acctTerm(pk)
			term.Origin = model.TermOriginManual
			return r.RestoreDictTerm(ctx, term, 2, m)
		}, false},
		{"DeleteManualTerm", func(r *DictionaryRepository, pk string, m *MembershipCheck) error {
			return r.DeleteManualTerm(ctx, pk, acctKey, 2, m)
		}, false},
		{"DeleteTombstone", func(r *DictionaryRepository, pk string, m *MembershipCheck) error {
			return r.DeleteTombstone(ctx, pk, acctKey, 2, m)
		}, false},
	}
}

// memberCheckIndex returns the transaction index of the membership
// ConditionCheck, or -1 when the call has none.
func memberCheckIndex(t *testing.T, call dictCall) int {
	t.Helper()
	if call.op != "TransactWriteItems" {
		return -1
	}
	for i, item := range txItems(t, call) {
		check, ok := item["ConditionCheck"].(map[string]any)
		if !ok {
			continue
		}
		key := check["Key"].(map[string]any)
		if key["SK"].(map[string]any)["S"] == model.PrefixMember+"u1" {
			if key["PK"].(map[string]any)["S"] != model.PrefixAccount+"a1" {
				t.Fatalf("membership check on wrong account: %v", key)
			}
			requireAll(t, "member check", resolved(check, "ConditionExpression"), "attribute_exists")
			return i
		}
	}
	return -1
}

func TestAccountTermWriteRequiresMembership(t *testing.T) {
	member := &MembershipCheck{AccountID: "a1", UserID: "u1"}
	for _, w := range acctWrites() {
		t.Run(w.name+"/member", func(t *testing.T) {
			repo, calls := dictWire(t, nil)
			if err := w.run(repo, "ACCOUNT#a1", member); err != nil {
				t.Fatal(err)
			}
			// Tombstones follow with a best-effort observation cleanup Query.
			if len(*calls) == 0 || memberCheckIndex(t, (*calls)[0]) < 0 {
				t.Fatalf("the first call must be a transaction carrying the membership check: %+v", *calls)
			}
		})
		t.Run(w.name+"/non-member", func(t *testing.T) {
			var idx int
			repo, calls := dictWire(t, func(_ int, _ string, body map[string]any) (int, string) {
				n := len(body["TransactItems"].([]any))
				codes := make([]string, n)
				for i := range codes {
					codes[i] = "None"
				}
				codes[idx] = "ConditionalCheckFailed"
				return 400, cancelled(codes...)
			})
			// Discover where the check sits by running once against a recording repo.
			probe, probeCalls := dictWire(t, nil)
			if err := w.run(probe, "ACCOUNT#a1", member); err != nil {
				t.Fatal(err)
			}
			idx = memberCheckIndex(t, (*probeCalls)[0])
			err := w.run(repo, "ACCOUNT#a1", member)
			if !errors.Is(err, ErrNotAccountMember) {
				t.Fatalf("want ErrNotAccountMember, got %v", err)
			}
			if errors.Is(err, ErrDictTermConflict) {
				t.Fatalf("membership failure must not look like a term conflict: %v", err)
			}
			if len(*calls) != 1 || (*calls)[0].op != "TransactWriteItems" {
				t.Fatalf("a rejected write must be one cancelled transaction: %+v", *calls)
			}
		})
		for name, m := range map[string]*MembershipCheck{
			"nil member":       nil,
			"other account":    {AccountID: "a2", UserID: "u1"},
			"missing user":     {AccountID: "a1"},
			"empty account ID": {UserID: "u1"},
		} {
			t.Run(w.name+"/"+name, func(t *testing.T) {
				repo, calls := dictWire(t, nil)
				if err := w.run(repo, "ACCOUNT#a1", m); !errors.Is(err, ErrInvalidInput) {
					t.Fatalf("want ErrInvalidInput, got %v", err)
				}
				if len(*calls) != 0 {
					t.Fatalf("nothing may be written: %+v", *calls)
				}
			})
		}
	}
}

func TestPersonalAndCommonWritesNeedNoMember(t *testing.T) {
	for _, pk := range []string{"USER#u1", model.PrefixDictCommon} {
		for _, w := range acctWrites() {
			t.Run(w.name+"/"+strings.TrimSuffix(pk, "#u1"), func(t *testing.T) {
				repo, calls := dictWire(t, nil)
				if err := w.run(repo, pk, nil); err != nil {
					t.Fatal(err)
				}
				if len(*calls) == 0 {
					t.Fatal("no write issued")
				}
				for _, c := range *calls {
					if memberCheckIndex(t, c) >= 0 {
						t.Fatalf("%s write must not carry a membership check", pk)
					}
					if c.op == "TransactWriteItems" != w.transactional {
						t.Fatalf("%s write changed shape: %s", pk, c.op)
					}
				}
			})
		}
	}
}

func TestLegacyMigrationWritesRejectAccountScope(t *testing.T) {
	ctx := context.Background()
	guard := LegacyBlobGuard{UserID: "u1"}
	term, _ := acctTerm("ACCOUNT#a1")
	term.LegacyMigrated = true
	mixed, _ := acctTerm("USER#u1")
	repo, calls := dictWire(t, nil)
	if _, err := repo.PutDictTermsIfAbsent(ctx, guard, []model.DictTerm{*mixed, *term}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("PutDictTermsIfAbsent: want ErrInvalidInput, got %v", err)
	}
	if err := repo.ReplaceLegacyMigratedTerm(ctx, guard, "ACCOUNT#a1", acctKey, 1, term); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ReplaceLegacyMigratedTerm (replace): want ErrInvalidInput, got %v", err)
	}
	if err := repo.ReplaceLegacyMigratedTerm(ctx, guard, "ACCOUNT#a1", acctKey, 1, nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ReplaceLegacyMigratedTerm (delete): want ErrInvalidInput, got %v", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("nothing may be written: %+v", *calls)
	}
}

func TestMalformedAccountPartitionFailsClosed(t *testing.T) {
	for _, w := range acctWrites() {
		t.Run(w.name, func(t *testing.T) {
			repo, calls := dictWire(t, nil)
			err := w.run(repo, "ACCOUNT#", &MembershipCheck{AccountID: "", UserID: "u1"})
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("want ErrInvalidInput, got %v", err)
			}
			if len(*calls) != 0 {
				t.Fatalf("nothing may be written: %+v", *calls)
			}
		})
	}
}
