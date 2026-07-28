package sharebackfill

import "testing"

func TestClassify(t *testing.T) {
	cases := []struct {
		name      string
		accountID string
		meeting   MeetingRefState
		share     ShareState
		want      Verdict
	}{
		{
			name:      "untagged share on a genuinely account-shared meeting is a candidate",
			accountID: "acc-1",
			meeting:   MeetingRefState{SharedToAccount: true, AccountID: "acc-1", UploaderUserID: "owner-1"},
			share:     ShareState{SharedToID: "tam-1", Origin: ""},
			want:      VerdictCandidate,
		},
		{
			name:      "already-tagged account-origin share needs no action",
			accountID: "acc-1",
			meeting:   MeetingRefState{SharedToAccount: true, AccountID: "acc-1", UploaderUserID: "owner-1"},
			share:     ShareState{SharedToID: "tam-1", Origin: "account"},
			want:      VerdictSkip,
		},
		{
			name:      "the meeting's own uploader is never a ShareMeetingToAccount recipient",
			accountID: "acc-1",
			meeting:   MeetingRefState{SharedToAccount: true, AccountID: "acc-1", UploaderUserID: "owner-1"},
			share:     ShareState{SharedToID: "owner-1", Origin: ""},
			want:      VerdictSkip,
		},
		{
			name:      "un-shared meeting (SharedToAccount false) with untagged share is orphaned",
			accountID: "acc-1",
			meeting:   MeetingRefState{SharedToAccount: false, AccountID: "acc-1", UploaderUserID: "owner-1"},
			share:     ShareState{SharedToID: "tam-1", Origin: ""},
			want:      VerdictOrphaned,
		},
		{
			name:      "meeting re-shared to a different account leaves the untagged share orphaned for this account",
			accountID: "acc-1",
			meeting:   MeetingRefState{SharedToAccount: true, AccountID: "acc-2", UploaderUserID: "owner-1"},
			share:     ShareState{SharedToID: "tam-1", Origin: ""},
			want:      VerdictOrphaned,
		},
		{
			name:      "un-shared meeting with an already-tagged share is not orphaned (nothing untagged to report)",
			accountID: "acc-1",
			meeting:   MeetingRefState{SharedToAccount: false, AccountID: "acc-1", UploaderUserID: "owner-1"},
			share:     ShareState{SharedToID: "tam-1", Origin: "account"},
			want:      VerdictSkip,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Classify(c.accountID, c.meeting, c.share)
			if got != c.want {
				t.Errorf("Classify(%q, %+v, %+v) = %v, want %v", c.accountID, c.meeting, c.share, got, c.want)
			}
		})
	}
}

func TestParseExclude(t *testing.T) {
	t.Run("empty string yields an empty set", func(t *testing.T) {
		got, err := ParseExclude("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("expected empty set, got %v", got)
		}
	})

	t.Run("single pair", func(t *testing.T) {
		got, err := ParseExclude("user-1:meeting-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got["user-1:meeting-1"] {
			t.Errorf("expected user-1:meeting-1 to be excluded, got %v", got)
		}
	})

	t.Run("multiple pairs with surrounding whitespace", func(t *testing.T) {
		got, err := ParseExclude(" user-1:meeting-1 , user-2:meeting-2 ")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 || !got["user-1:meeting-1"] || !got["user-2:meeting-2"] {
			t.Errorf("expected both pairs excluded, got %v", got)
		}
	})

	t.Run("trailing comma is ignored, not a malformed entry", func(t *testing.T) {
		got, err := ParseExclude("user-1:meeting-1,")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Errorf("expected 1 entry, got %v", got)
		}
	})

	t.Run("missing colon is a format error", func(t *testing.T) {
		_, err := ParseExclude("user-1-meeting-1")
		if err == nil {
			t.Fatal("expected an error for a missing colon separator")
		}
		fmtErr, ok := err.(*ExcludeFormatError)
		if !ok {
			t.Fatalf("expected *ExcludeFormatError, got %T (%v)", err, err)
		}
		if fmtErr.Entry != "user-1-meeting-1" {
			t.Errorf("expected Entry=%q, got %q", "user-1-meeting-1", fmtErr.Entry)
		}
	})
}
