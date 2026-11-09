package model

import "testing"

func TestIsNewOpportunityCandidate(t *testing.T) {
	tests := []struct {
		name   string
		doc    CrawledDocument
		linked bool
		want   bool
	}{
		{"at threshold", CrawledDocument{OpportunityCount: 1, QualityScore: 0.75}, false, true},
		{"just below threshold", CrawledDocument{OpportunityCount: 1, QualityScore: 0.7499}, false, false},
		{"no opportunities", CrawledDocument{OpportunityCount: 0, QualityScore: 0.99}, false, false},
		{"linked", CrawledDocument{OpportunityCount: 3, QualityScore: 0.9}, true, false},
		{"legacy row", CrawledDocument{}, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsNewOpportunityCandidate(tt.doc, tt.linked); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}
