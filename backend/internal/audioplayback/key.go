// Package audioplayback names the playback copies of meeting audio (ADR-062).
//
// Browser MediaRecorder WebM/Ogg lacks Duration and Cues, and an interrupted
// recording can end mid-cluster. A remuxed copy under a separate top-level
// prefix restores seeking without touching the original, which STT, crop,
// checkpoint and recovery keep using. The separate prefix also keeps the copy's
// own S3 event out of the audio/ transcription rule.
package audioplayback

import (
	"path"
	"strings"
)

const (
	SourcePrefix   = "audio"
	PlaybackPrefix = "audio-playback"

	// Object metadata on a playback copy binding it to its exact source.
	MetaSourceETag      = "source-etag"
	MetaSourceVersionID = "source-version-id"
)

var remuxExtensions = map[string]bool{".webm": true, ".ogg": true}

// splitSource validates `audio/{userId}/{meetingId}/{name}` and returns the segments.
func splitSource(key string) (userID, meetingID, name string, ok bool) {
	if strings.ContainsAny(key, "\\\x00%") {
		return "", "", "", false
	}
	parts := strings.Split(key, "/")
	if len(parts) != 4 || parts[0] != SourcePrefix {
		return "", "", "", false
	}
	for _, segment := range parts[1:] {
		if segment == "" || segment == "." || segment == ".." || strings.TrimSpace(segment) != segment {
			return "", "", "", false
		}
	}
	return parts[1], parts[2], parts[3], true
}

// IsRemuxSource reports whether a source key should receive a playback copy:
// a WebM/Ogg meeting recording that is not a live checkpoint. Checkpoints are
// overwritten while recording; recovered copies (recording_recovered_*) are
// final and included.
func IsRemuxSource(key string) bool {
	_, _, name, ok := splitSource(key)
	if !ok || strings.HasPrefix(name, "recording_progress.") {
		return false
	}
	return remuxExtensions[strings.ToLower(path.Ext(name))]
}

// KeyFor returns the playback-copy key for a source key, or false when the
// source does not get one. The mapping is deterministic so the worker replaces
// the copy conditionally instead of accumulating objects.
func KeyFor(sourceKey string) (string, bool) {
	if !IsRemuxSource(sourceKey) {
		return "", false
	}
	return PlaybackPrefix + strings.TrimPrefix(sourceKey, SourcePrefix), true
}

// SourceFor inverts KeyFor for a valid playback key.
func SourceFor(playbackKey string) (string, bool) {
	if !strings.HasPrefix(playbackKey, PlaybackPrefix+"/") {
		return "", false
	}
	source := SourcePrefix + strings.TrimPrefix(playbackKey, PlaybackPrefix)
	if !IsRemuxSource(source) {
		return "", false
	}
	return source, true
}

// MatchesSource reports whether playback-copy metadata names the source's
// current version. Both identifiers must be present and equal; S3 returns
// user metadata keys in lower case.
func MatchesSource(metadata map[string]string, sourceETag, sourceVersionID string) bool {
	if sourceETag == "" || sourceVersionID == "" {
		return false
	}
	return metadata[MetaSourceETag] == sourceETag && metadata[MetaSourceVersionID] == sourceVersionID
}
