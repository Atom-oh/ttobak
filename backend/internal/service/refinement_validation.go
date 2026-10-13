package service

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/speaker"
)

var (
	errRefinementIncomplete = errors.New("refinement response incomplete")
	errRefinementSchema     = errors.New("refinement schema invalid")
	errRefinementTimeline   = errors.New("refinement timeline invalid")
	errRefinementCoverage   = errors.New("refinement source coverage invalid")
	errRefinementInput      = errors.New("refinement input invalid")
)

func validRefinementInput(input []WhisperSegment) bool {
	if len(input) == 0 {
		return false
	}
	for i, segment := range input {
		if !finiteTime(segment.Start) || !finiteTime(segment.End) ||
			segment.End <= segment.Start || strings.TrimSpace(segment.Text) == "" ||
			i > 0 && segment.Start < input[i-1].Start {
			return false
		}
	}
	return true
}

func finiteTime(t float64) bool { return !math.IsNaN(t) && !math.IsInf(t, 0) && t >= 0 }

// refinementMinDuration widens a zero-length segment that Whisper rounding collapsed,
// so its words still reach refinement instead of invalidating the meeting.
const refinementMinDuration = 0.01

// normalizeRefinementInput returns a copy without blank segments and with zero-length
// ones widened. Blank text carries no words; other timing faults keep chunk fallback.
func normalizeRefinementInput(input []WhisperSegment) []WhisperSegment {
	out := make([]WhisperSegment, 0, len(input))
	for _, segment := range input {
		if strings.TrimSpace(segment.Text) == "" {
			continue
		}
		if finiteTime(segment.Start) && finiteTime(segment.End) && segment.End == segment.Start {
			segment.End += refinementMinDuration
		}
		out = append(out, segment)
	}
	return out
}

func anyValidRefinementChunk(chunks [][]WhisperSegment) bool {
	for _, chunk := range chunks {
		if validRefinementInput(chunk) {
			return true
		}
	}
	return false
}

func validInferredSpeaker(label string) bool {
	number, err := strconv.Atoi(strings.TrimPrefix(label, "spk_"))
	return err == nil && number >= 0 && number < speaker.MaxPerPart && label == "spk_"+strconv.Itoa(number)
}

func refinementCompletion(body []byte) (string, error) {
	if len(body) > 512*1024 {
		return "", errRefinementSchema
	}
	var response ClaudeResponse
	if json.Unmarshal(body, &response) != nil {
		return "", errRefinementParse
	}
	if response.StopReason != "end_turn" {
		return "", errRefinementIncomplete
	}
	var text strings.Builder
	for _, block := range response.Content {
		if block.Type != "text" {
			return "", errRefinementIncomplete
		}
		text.WriteString(block.Text)
	}
	if text.Len() == 0 || text.Len() > 256*1024 {
		return "", errRefinementSchema
	}
	return text.String(), nil
}

func validateRefinementJSON(text string, input []WhisperSegment) ([]RefinedSegment, error) {
	type wireSegment struct {
		Speaker   *string  `json:"speaker"`
		Text      *string  `json:"text"`
		Start     *float64 `json:"startTime"`
		End       *float64 `json:"endTime"`
		SourceIDs []*int   `json:"sourceIds"`
	}
	var wire []wireSegment
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil {
		return nil, errRefinementParse
	}
	if decoder.Decode(new(any)) != io.EOF || len(wire) == 0 || len(wire) > len(input) {
		return nil, errRefinementSchema
	}
	nextID := 0
	result := make([]RefinedSegment, 0, len(wire))
	for _, segment := range wire {
		if segment.Speaker == nil || segment.Text == nil || segment.Start == nil || segment.End == nil ||
			strings.TrimSpace(*segment.Text) == "" || strings.TrimSpace(*segment.Speaker) == "" ||
			len(*segment.Speaker) > 128 || len(segment.SourceIDs) == 0 {
			return nil, errRefinementSchema
		}
		first := nextID
		maxEnd := 0.0
		for _, sourceID := range segment.SourceIDs {
			// Every input appears exactly once, in order. This prohibits
			// missing, duplicated, foreign, reordered and invented IDs.
			if sourceID == nil || *sourceID != nextID || nextID >= len(input) {
				return nil, errRefinementCoverage
			}
			maxEnd = math.Max(maxEnd, input[nextID].End)
			nextID++
		}
		if !finiteTime(*segment.Start) || !finiteTime(*segment.End) || *segment.End <= *segment.Start ||
			math.Abs(*segment.Start-input[first].Start) > 0.000001 ||
			math.Abs(*segment.End-maxEnd) > 0.000001 {
			return nil, errRefinementTimeline
		}
		result = append(result, RefinedSegment{Speaker: *segment.Speaker, Text: *segment.Text,
			StartTime: *segment.Start, EndTime: *segment.End})
	}
	if nextID != len(input) {
		return nil, errRefinementCoverage
	}
	return result, nil
}

func refinementQuality(segments []RefinedSegment, chunks int) *model.TranscriptQuality {
	quality := &model.TranscriptQuality{Refinement: "validated", TotalChunks: chunks}
	fallbacks, reasons, provenance := map[int]bool{}, map[string]bool{}, map[string]bool{}
	for _, segment := range segments {
		if segment.FallbackCode != "" {
			fallbacks[segment.ChunkIndex], reasons[segment.FallbackCode] = true, true
		}
		provenance[segment.SpeakerProvenance] = true
	}
	quality.FallbackChunks = len(fallbacks)
	if len(fallbacks) > 0 {
		quality.Refinement, quality.Degraded = "partial", true
		if len(fallbacks) == chunks {
			quality.Refinement = "raw"
		}
	}
	quality.SpeakerProvenance = "unknown"
	if len(provenance) > 1 {
		quality.SpeakerProvenance = "mixed"
	} else {
		for source := range provenance {
			quality.SpeakerProvenance = source
		}
	}
	if quality.SpeakerProvenance != "acoustic" {
		quality.Degraded = true
		reasons["diarization_unavailable"] = true
	}
	for reason := range reasons {
		quality.Reasons = append(quality.Reasons, reason)
	}
	sort.Strings(quality.Reasons)
	return quality
}
