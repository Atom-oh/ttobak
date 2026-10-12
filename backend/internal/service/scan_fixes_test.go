package service

import (
	"context"
	"errors"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func TestCreateResearchInvalidModeIsASentinel(t *testing.T) {
	_, err := (&ResearchService{}).CreateResearch(context.Background(), "u", &model.CreateResearchRequest{Mode: "turbo"})
	if !errors.Is(err, ErrInvalidResearchMode) {
		t.Fatalf("err = %v, want ErrInvalidResearchMode", err)
	}
}

func TestRecoverRefineChunkFallsBackToRawSegmentsOnPanic(t *testing.T) {
	done := make(chan chunkResult, 1)
	segments := []WhisperSegment{{Start: 1, End: 2, Text: "원문"}}
	func() {
		defer recoverRefineChunk(3, 5, done, segments, nil)
		panic("synthetic refinement panic")
	}()
	select {
	case r := <-done:
		if r.index != 3 || len(r.segments) != 1 || r.segments[0].Text != "원문" {
			t.Fatalf("fallback = %+v", r)
		}
	default:
		t.Fatal("a panicking chunk must still deliver one result")
	}
	// No panic: nothing is sent, so the normal path's own send stays the only one.
	func() { defer recoverRefineChunk(0, 1, done, segments, nil) }()
	if len(done) != 0 {
		t.Fatal("recover sent a result without a panic")
	}
}
