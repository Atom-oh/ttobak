package repository

import (
	"context"
	"strings"
	"testing"
)

func batchMeetingRow(owner, id string) map[string]any {
	return map[string]any{"PK": map[string]any{"S": "USER#" + owner}, "SK": map[string]any{"S": "MEETING#" + id},
		"meetingId": map[string]any{"S": id}, "userId": map[string]any{"S": owner}, "title": map[string]any{"S": "Title " + id},
		"sharedToAccount": map[string]any{"BOOL": true}}
}

func TestBatchGetMeetingListRows(t *testing.T) {
	for _, tc := range []struct {
		name     string
		keys     []MeetingKey
		extra    map[string]any
		wantErr  string
		want     []string
		requests int
	}{
		{name: "empty", requests: 0},
		{name: "rows keyed by request, missing rows absent", keys: []MeetingKey{{"o1", "m1"}, {"o2", "m2"}, {"o1", "missing"}, {"o1", "m1"}},
			want: []string{"o1/m1", "o2/m2"}, requests: 1},
		{name: "invalid key", keys: []MeetingKey{{"", "m1"}}, wantErr: "invalid meeting row key"},
		{name: "unrequested row fails closed", keys: []MeetingKey{{"o1", "m1"}}, extra: batchMeetingRow("o9", "m9"),
			wantErr: "unexpected batch read response", requests: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := &batchWire{rows: map[batchWireKey]map[string]any{
				{"USER#o1", "MEETING#m1"}: batchMeetingRow("o1", "m1"),
				{"USER#o2", "MEETING#m2"}: batchMeetingRow("o2", "m2"),
			}, extra: tc.extra}
			rows, err := wire.repo(t).BatchGetMeetingListRows(context.Background(), tc.keys)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if len(wire.requests) != tc.requests {
				t.Fatalf("requests = %d, want %d", len(wire.requests), tc.requests)
			}
			for i := range wire.requests {
				if !wire.consistent[i] || wire.projections[i] != strings.Join(MeetingListRowFields, ", ") {
					t.Fatalf("request %d consistent=%v projection=%q", i, wire.consistent[i], wire.projections[i])
				}
			}
			if tc.wantErr != "" {
				return
			}
			if len(rows) != len(tc.want) {
				t.Fatalf("rows = %v, want %v", rows, tc.want)
			}
			for _, want := range tc.want {
				owner, id, _ := strings.Cut(want, "/")
				m := rows[MeetingKey{owner, id}]
				if m == nil || m.UserID != owner || m.MeetingID != id || !m.SharedToAccount {
					t.Fatalf("row %s = %+v", want, m)
				}
			}
		})
	}
}
