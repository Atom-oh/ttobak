package main

import (
	"github.com/ttobak/backend/internal/model"
	"testing"
)

func TestExtractPartIndex(t *testing.T) {
	cases := []struct {
		name    string
		key     string
		wantIdx int
		wantOk  bool
	}{
		{
			name:    "multipart key at basename start",
			key:     "audio/u1/m1/part_002_chunk.wav",
			wantIdx: 2,
			wantOk:  true,
		},
		{
			name:    "multipart key at top level",
			key:     "part_007_x.wav",
			wantIdx: 7,
			wantOk:  true,
		},
		{
			name:    "single upload that happens to contain 'part_NNN_' mid-name is rejected",
			key:     "audio/u1/m1/song_part_002_remix.wav",
			wantIdx: 0,
			wantOk:  false,
		},
		{
			name:    "no part prefix",
			key:     "audio/u1/m1/recording.wav",
			wantIdx: 0,
			wantOk:  false,
		},
		{
			name:    "wrong digit count (2 digits) — must be 3",
			key:     "audio/u1/m1/part_02_x.wav",
			wantIdx: 0,
			wantOk:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idx, ok := extractPartIndex(tc.key)
			if ok != tc.wantOk || idx != tc.wantIdx {
				t.Fatalf("extractPartIndex(%q) = (%d, %v), want (%d, %v)",
					tc.key, idx, ok, tc.wantIdx, tc.wantOk)
			}
		})
	}
}

func TestRecoveredAudioEventWaitsForCanonicalBinding(t *testing.T) {
	key := "audio/owner/meeting/recording_recovered_0123456789abcdef0123456789abcdef.webm"
	for _, tc := range []struct {
		name        string
		meeting     *model.Meeting
		skip, retry bool
	}{
		{"deleted", nil, true, false},
		{"copy arrived before binding", &model.Meeting{}, false, true},
		{"bound winner", &model.Meeting{AudioKey: key}, false, false},
		{"different completed upload", &model.Meeting{AudioKey: "audio/owner/meeting/new.webm"}, true, false},
		{"multipart winner", &model.Meeting{AudioKeys: []string{key, "another"}}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			skip, err := recoveryAudioEvent(tc.meeting, key)
			if skip != tc.skip || (err != nil) != tc.retry {
				t.Fatalf("skip=%v err=%v", skip, err)
			}
		})
	}
	if skip, err := recoveryAudioEvent(nil, "audio/owner/meeting/ordinary.webm"); skip || err != nil {
		t.Fatal("ordinary upload changed")
	}
}
