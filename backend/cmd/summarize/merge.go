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

	var allTexts []string
	var allSegments []TranscriptSegmentOut
	var cumulativeOffset float64

	var parseFailures int
	for _, part := range parts {
		transcript, segments, whisperSegments, parseErr := downloadAndParseTranscript(ctx, bucket, part.key)
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

		// Determine this part's duration from its last segment
		var partDuration float64
		if len(segments) > 0 {
			partDuration = segments[len(segments)-1].EndTime
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
