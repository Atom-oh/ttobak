package service

import (
	"context"
	"fmt"
	"log"
	"sort"

	"github.com/ttobak/backend/internal/model"
)

// WhisperPromptTerms returns the display forms for one meeting's Whisper
// initial prompt, in priority order: Personal manual, the meeting's Account
// (only while userID is a current member), Common, Personal auto. The caller
// cuts the list at the prompt's rune cap, so the order decides what survives.
//
// Only active rows count. An Account read that cannot be authorized (no
// membership reader, a membership read error) or read drops that layer and
// logs, and so does a Common read error: neither fails the prompt, which then
// keeps the Personal terms it had before the shared layers existed. A
// Personal read error is returned.
func (s *DictionaryService) WhisperPromptTerms(ctx context.Context, userID, accountID string) ([]string, error) {
	if userID == "" {
		return nil, nil
	}
	dict, err := s.repo.GetDictionary(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get dictionary: %w", err)
	}
	personal, err := s.repo.ListDictTerms(ctx, model.PrefixUser+userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get dictionary terms: %w", err)
	}
	autoKeys := map[string]bool{}
	for _, row := range personal {
		if row.State == model.TermStateActive && row.Origin == model.TermOriginAuto {
			autoKeys[row.TermKey] = true
		}
	}
	var manual, auto []string
	for _, t := range unionLegacyTerms(dict, personal) {
		display := termDisplay(t.DisplayAs, t.Phrase)
		if autoKeys[legacyTermKey(t)] {
			auto = append(auto, display)
		} else {
			manual = append(manual, display)
		}
	}

	out := manual
	if accountID != "" {
		if err := s.requireMember(ctx, accountID, userID); err != nil {
			log.Printf("whisper prompt: skipping account layer for user %s: %v", userID, err)
		} else if rows, err := s.repo.ListDictTerms(ctx, model.PrefixAccount+accountID); err != nil {
			log.Printf("whisper prompt: account terms read failed for user %s: %v", userID, err)
		} else {
			out = append(out, activeDisplays(rows)...)
		}
	}
	if common, err := s.repo.ListDictTerms(ctx, model.PrefixDictCommon); err != nil {
		log.Printf("whisper prompt: skipping common layer for user %s: %v", userID, err)
	} else {
		out = append(out, activeDisplays(common)...)
	}
	return append(out, auto...), nil
}

func termDisplay(displayAs, phrase string) string {
	if displayAs == "" {
		return phrase
	}
	return displayAs
}

// activeDisplays lists the display forms of active rows with one, oldest
// first.
func activeDisplays(rows []model.DictTerm) []string {
	active := make([]model.DictTerm, 0, len(rows))
	for _, row := range rows {
		if row.State == model.TermStateActive && termDisplay(row.DisplayAs, row.Phrase) != "" {
			active = append(active, row)
		}
	}
	sort.SliceStable(active, func(i, j int) bool {
		if active[i].AddedAt != active[j].AddedAt {
			return active[i].AddedAt < active[j].AddedAt
		}
		return active[i].SK < active[j].SK
	})
	out := make([]string, 0, len(active))
	for _, row := range active {
		out = append(out, termDisplay(row.DisplayAs, row.Phrase))
	}
	return out
}
