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

	var parseFailures int
	for _, part := range parts {
		transcript, segments, whisperSegments, audioDuration, parseErr := downloadAndParseTranscript(ctx, bucket, part.key)
		if parseErr != nil {
			log.Printf("Failed to parse part %d transcript: %v", part.index, parseErr)
			parseFailures++
			continue
		}

		if len(whisperSegments) > 0 && len(segments) == 0 {
			refinedText, refinedSegs, refineErr := bedrockService.RefineTranscript(ctx, whisperSegments)
			if refineErr == nil {
				transcript = refinedText
				segments = make([]TranscriptSegmentOut, len(refinedSegs))
				for i, rs := range refinedSegs {
					segments[i] = TranscriptSegmentOut{
						Speaker:   rs.Speaker,
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
	if parseFailures > 0 {
		return "", nil, fmt.Errorf("merge aborted: %d of %d parts failed to parse for meeting %s", parseFailures, len(parts), meetingID)
	}

	mergedText := strings.Join(allTexts, "\n\n")

	log.Printf("Merged %d parts for meeting %s: %d chars, %d segments, total duration %.0fs",
		len(parts), meetingID, len(mergedText), len(allSegments), cumulativeOffset)

	return mergedText, allSegments, nil
}
