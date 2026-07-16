package main

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

var partKeyPattern = regexp.MustCompile(`_part_(\d{3})\.json$`)

// maxSpeakersPerPart bounds n before offsetting -- without this, an n that
// itself already exceeds the offset step (implausible from _assign_speakers'
// normalization in practice, but not something this function can assume)
// would let one part's namespaced range collide with another's. Enforcing
// the bound makes collision-freedom a property of this function alone, not
// a fact about pyannote's typical output that could change.
const maxSpeakersPerPart = 1_000_000

// namespaceSpeakerLabel rewrites an acoustic spk_N label to be unique across
// parts by offsetting N by partIndex*maxSpeakersPerPart. Stays within
// spk_\d+ so the frontend's SpeakerMapEditor (UNMAPPED_PATTERN,
// speakerSortKey's parseInt) needs no changes. Non-spk_ labels pass through
// unchanged.
func namespaceSpeakerLabel(label string, partIndex int) string {
	if !strings.HasPrefix(label, "spk_") {
		return label
	}
	n, err := strconv.Atoi(strings.TrimPrefix(label, "spk_"))
	if err != nil {
		return label // not a spk_N label (e.g. a Korean 화자 fallback) -- leave as-is
	}
	// Clamp BEFORE computing the offset, and apply the offset uniformly
	// (including partIndex==0) so every part's output is confined to the
	// disjoint interval [partIndex*maxSpeakersPerPart,
	// (partIndex+1)*maxSpeakersPerPart - 1] regardless of the input n.
	// _assign_speakers only ever produces sequential labels starting at 0,
	// so n reaching this clamp in practice would mean something already
	// went wrong upstream; the clamp exists so out-of-range input degrades
	// to a shared bucket within its own part rather than colliding with
	// another part's range.
	switch {
	case n < 0:
		n = 0
	case n >= maxSpeakersPerPart:
		n = maxSpeakersPerPart - 1
	}
	return fmt.Sprintf("spk_%d", partIndex*maxSpeakersPerPart+n)
}

// mergePartTranscripts downloads all part transcripts, refines each, offsets timestamps, and concatenates them
func mergePartTranscripts(ctx context.Context, bucket, meetingID string, partCount int) (string, []TranscriptSegmentOut, error) {
	prefix := fmt.Sprintf("transcripts/%s_part_", meetingID)

	result, err := s3Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
		Prefix: aws.String(prefix),
	})
	if err != nil {
		return "", nil, fmt.Errorf("failed to list part transcripts: %w", err)
	}

	if len(result.Contents) == 0 {
		return "", nil, fmt.Errorf("no part transcripts found for meeting %s", meetingID)
	}

	type partFile struct {
		key   string
		index int
	}

	var parts []partFile
	for _, obj := range result.Contents {
		key := *obj.Key
		matches := partKeyPattern.FindStringSubmatch(key)
		if len(matches) < 2 {
			continue
		}
		idx, _ := strconv.Atoi(matches[1])
		parts = append(parts, partFile{key: key, index: idx})
	}

	sort.Slice(parts, func(i, j int) bool {
		return parts[i].index < parts[j].index
	})

	// Guard against silent data loss: S3 list can return fewer keys than
	// `partCount` (eventual consistency lag, accidental delete, etc.).
	// Bailing here is safer than merging a partial timeline. The Lambda
	// runtime will retry on returned errors and AllPartsTranscribed events
	// are claimed once-only via `allPartsEmittedAt`, so a transient lag
	// resolves on the next invocation.
	if len(parts) != partCount {
		return "", nil, fmt.Errorf(
			"part count mismatch for meeting %s: expected %d, got %d (S3 list lag or missing parts)",
			meetingID, partCount, len(parts),
		)
	}
	// Verify dense contiguous indices [0, partCount). A gap means one of
	// the expected parts was never uploaded or its key doesn't match the
	// expected pattern.
	for i, p := range parts {
		if p.index != i {
			return "", nil, fmt.Errorf(
				"part index gap for meeting %s: expected index %d, got %d",
				meetingID, i, p.index,
			)
		}
	}

	var allTexts []string
	var allSegments []TranscriptSegmentOut
	var cumulativeOffset float64

	for _, part := range parts {
		transcript, segments, whisperSegments, audioDuration, parseErr := downloadAndParseTranscript(ctx, bucket, part.key)
		if parseErr != nil {
			// Fail fast: continuing would download/refine the rest of the
			// parts only to abort at the end (the prior loop drained S3 +
			// Bedrock budget on a doomed merge). The merge is all-or-nothing.
			return "", nil, fmt.Errorf("part %d parse failed for meeting %s: %w", part.index, meetingID, parseErr)
		}

		if len(whisperSegments) > 0 && len(segments) == 0 {
			refinedText, refinedSegs, refineErr := bedrockService.RefineTranscript(ctx, whisperSegments)
			if refineErr == nil {
				transcript = refinedText
				// Acoustic (preserve-mode) diarization runs per-part, so each
				// part's spk_N numbering restarts at 0 independently -- without
				// namespacing, part 1's spk_0 and part 2's spk_0 would refer to
				// different real speakers but display as the same one after
				// merge. Only namespace when this part actually used acoustic
				// labels (any non-empty WhisperSegment.Speaker); infer-mode
				// parts have no acoustic authority to preserve here.
				acoustic := false
				for _, ws := range whisperSegments {
					if ws.Speaker != "" {
						acoustic = true
						break
					}
				}
				segments = make([]TranscriptSegmentOut, len(refinedSegs))
				for i, rs := range refinedSegs {
					speaker := rs.Speaker
					if acoustic {
						speaker = namespaceSpeakerLabel(speaker, part.index)
					}
					segments[i] = TranscriptSegmentOut{
						Speaker:   speaker,
						Text:      rs.Text,
						StartTime: rs.StartTime,
						EndTime:   rs.EndTime,
					}
				}
			}
		}

		allTexts = append(allTexts, transcript)

		// Determine this part's duration with a fallback chain so a single
		// failed refinement doesn't silently collapse the rest of the
		// timeline onto offset=0:
		//   1. whisper_metadata.duration_seconds (preferred — true audio length
		//      including trailing silence; ADR-014 §5.2 mandates this)
		//   2. last refined segment's EndTime (covers older transcripts without
		//      whisper metadata; understates by trailing silence so subsequent
		//      parts drift earlier)
		//   3. last raw Whisper segment's End (covers RefineTranscript failure
		//      AND missing duration metadata)
		//   4. 0 (only when all sources are empty — part is effectively silent)
		var partDuration float64
		switch {
		case audioDuration > 0:
			partDuration = audioDuration
		case len(segments) > 0:
			partDuration = segments[len(segments)-1].EndTime
			log.Printf(
				"Part %d: whisper_metadata.duration_seconds missing; falling back to last segment EndTime=%.2fs (may drift)",
				part.index, partDuration,
			)
		case len(whisperSegments) > 0:
			partDuration = whisperSegments[len(whisperSegments)-1].End
			log.Printf(
				"Part %d had no refined segments and no duration; using raw Whisper end=%.2fs as duration",
				part.index, partDuration,
			)
		}

		// Offset segment timestamps and append to merged list
		for _, seg := range segments {
			allSegments = append(allSegments, TranscriptSegmentOut{
				Speaker:   seg.Speaker,
				Text:      seg.Text,
				StartTime: seg.StartTime + cumulativeOffset,
				EndTime:   seg.EndTime + cumulativeOffset,
			})
		}

		cumulativeOffset += partDuration
	}

	if len(allTexts) == 0 {
		return "", nil, fmt.Errorf("no valid transcripts found for meeting %s", meetingID)
	}

	mergedText := strings.Join(allTexts, "\n\n")

	log.Printf("Merged %d parts for meeting %s: %d chars, %d segments, total duration %.0fs",
		len(parts), meetingID, len(mergedText), len(allSegments), cumulativeOffset)

	return mergedText, allSegments, nil
}
