package service

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

func TestNormalizeCalendarEventSharing(t *testing.T) {
	hexKey := strings.Repeat("ab", 32)
	base := func(mutate func(*model.CalendarEventInput)) model.CalendarEventInput {
		event := model.CalendarEventInput{
			ExternalID: "ext-1", Title: "Synthetic", TimeZone: "UTC",
			StartsAt: time.Date(2026, 11, 9, 1, 0, 0, 0, time.UTC),
			EndsAt:   time.Date(2026, 11, 9, 2, 0, 0, 0, time.UTC),
		}
		mutate(&event)
		return event
	}
	tests := []struct {
		name     string
		mutate   func(*model.CalendarEventInput)
		wantErr  bool
		evidence []string
		cands    []string
	}{
		{"valid share sorted", func(e *model.CalendarEventInput) {
			e.ShareAccountID, e.ShareEvidence = "acc_1", []string{"title", "domain"}
			e.CandidateAccountIDs, e.SeriesKey = []string{"acc_3", "acc_2"}, hexKey
		}, false, []string{"domain", "title"}, []string{"acc_2", "acc_3"}},
		{"empty slices become nil", func(e *model.CalendarEventInput) {
			e.ShareEvidence, e.CandidateAccountIDs = []string{}, []string{}
		}, false, nil, nil},
		{"old payload", func(e *model.CalendarEventInput) {}, false, nil, nil},
		{"private alone", func(e *model.CalendarEventInput) { e.Private = true }, false, nil, nil},
		{"evidence without account", func(e *model.CalendarEventInput) { e.ShareEvidence = []string{"title"} }, true, nil, nil},
		{"unknown evidence", func(e *model.CalendarEventInput) {
			e.ShareAccountID, e.ShareEvidence = "acc_1", []string{"body"}
		}, true, nil, nil},
		{"duplicate evidence", func(e *model.CalendarEventInput) {
			e.ShareAccountID, e.ShareEvidence = "acc_1", []string{"title", "title"}
		}, true, nil, nil},
		{"six candidates", func(e *model.CalendarEventInput) {
			e.CandidateAccountIDs = []string{"a1", "a2", "a3", "a4", "a5", "a6"}
		}, true, nil, nil},
		{"duplicate candidates", func(e *model.CalendarEventInput) { e.CandidateAccountIDs = []string{"a1", "a1"} }, true, nil, nil},
		{"bad candidate id", func(e *model.CalendarEventInput) { e.CandidateAccountIDs = []string{"bad id"} }, true, nil, nil},
		{"bad share id", func(e *model.CalendarEventInput) { e.ShareAccountID = "bad id" }, true, nil, nil},
		{"private with share", func(e *model.CalendarEventInput) { e.Private, e.ShareAccountID = true, "acc_1" }, true, nil, nil},
		{"private with candidates", func(e *model.CalendarEventInput) {
			e.Private, e.CandidateAccountIDs = true, []string{"a1"}
		}, true, nil, nil},
		{"uppercase series key", func(e *model.CalendarEventInput) { e.SeriesKey = "ABC" }, true, nil, nil},
		{"uppercase hex series key", func(e *model.CalendarEventInput) { e.SeriesKey = strings.Repeat("AB", 32) }, true, nil, nil},
		{"share equals candidate", func(e *model.CalendarEventInput) {
			e.ShareAccountID, e.CandidateAccountIDs = "acc_1", []string{"acc_1"}
		}, true, nil, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeCalendarEvent(base(test.mutate))
			if test.wantErr {
				if !errors.Is(err, ErrInvalidInput) {
					t.Fatalf("error = %v, want ErrInvalidInput", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got.ShareEvidence, test.evidence) || !reflect.DeepEqual(got.CandidateAccountIDs, test.cands) {
				t.Fatalf("evidence=%#v candidates=%#v", got.ShareEvidence, got.CandidateAccountIDs)
			}
		})
	}
}
