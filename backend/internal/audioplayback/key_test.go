package audioplayback

import "testing"

func TestKeyFor(t *testing.T) {
	cases := []struct {
		source, want string
		ok           bool
	}{
		{"audio/u1/m1/1793_recording_1793.webm", "audio-playback/u1/m1/1793_recording_1793.webm", true},
		{"audio/u1/m1/recording_recovered_0123456789abcdef0123456789abcdef.webm", "audio-playback/u1/m1/recording_recovered_0123456789abcdef0123456789abcdef.webm", true},
		{"audio/u1/m1/part_001_1793_a.ogg", "audio-playback/u1/m1/part_001_1793_a.ogg", true},
		{"audio/u1/m1/UPPER.WEBM", "audio-playback/u1/m1/UPPER.WEBM", true},
		// Live checkpoints are overwritten during recording.
		{"audio/u1/m1/recording_progress.webm", "", false},
		{"audio/u1/m1/recording_progress.ogg", "", false},
		// Containers that already carry duration/index data.
		{"audio/u1/m1/a.m4a", "", false},
		{"audio/u1/m1/crop_result_0123456789abcdef0123456789abcdef.wav", "", false},
		{"audio/u1/m1/a.mp3", "", false},
		// Shape and traversal.
		{"audio/u1/a.webm", "", false},
		{"audio/u1/m1/x/a.webm", "", false},
		{"images/u1/m1/a.webm", "", false},
		{"audio/../m1/a.webm", "", false},
		{"audio/u1/./a.webm", "", false},
		{"audio/u1/m1/..", "", false},
		{"audio//m1/a.webm", "", false},
		{"audio/u1/m1/a%2Fb.webm", "", false},
		{"audio/u1/m1/a\\b.webm", "", false},
		{"audio/u1/m1/ a.webm", "", false},
		{"audio-playback/u1/m1/a.webm", "", false},
	}
	for _, tc := range cases {
		got, ok := KeyFor(tc.source)
		if got != tc.want || ok != tc.ok {
			t.Errorf("KeyFor(%q) = %q,%v; want %q,%v", tc.source, got, ok, tc.want, tc.ok)
		}
		if ok {
			back, backOK := SourceFor(got)
			if !backOK || back != tc.source {
				t.Errorf("SourceFor(%q) = %q,%v; want %q", got, back, backOK, tc.source)
			}
		}
	}
}

func TestSourceForRejectsNonPlaybackKeys(t *testing.T) {
	for _, key := range []string{"audio/u/m/a.webm", "audio-playbackx/u/m/a.webm", "audio-playback/u/m/recording_progress.webm", "audio-playback/u/m/a.wav", "audio-playback/u/../a.webm"} {
		if got, ok := SourceFor(key); ok {
			t.Errorf("SourceFor(%q) = %q, want rejection", key, got)
		}
	}
}

func TestMatchesSource(t *testing.T) {
	meta := map[string]string{MetaSourceETag: `"abc"`, MetaSourceVersionID: "v1"}
	if !MatchesSource(meta, `"abc"`, "v1") {
		t.Fatal("exact match rejected")
	}
	for name, args := range map[string][2]string{
		"etag changed":    {`"abd"`, "v1"},
		"version changed": {`"abc"`, "v2"},
		"no etag":         {"", "v1"},
		"no version":      {`"abc"`, ""},
	} {
		if MatchesSource(meta, args[0], args[1]) {
			t.Errorf("%s: matched", name)
		}
	}
	if MatchesSource(map[string]string{MetaSourceETag: `"abc"`}, `"abc"`, "v1") {
		t.Error("metadata without a version matched")
	}
	if MatchesSource(nil, `"abc"`, "v1") {
		t.Error("nil metadata matched")
	}
}
