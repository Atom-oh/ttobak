package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

// TermsRequested must reach the terms worker, never the S3 transcript path.
func TestHandlerRoutesTermsRequestedToTheWorker(t *testing.T) {
	restore := processTermsRequested
	defer func() { processTermsRequested = restore }()
	sentinel := errors.New("terms worker reached")
	var got model.TermsRequested
	processTermsRequested = func(_ context.Context, event model.TermsRequested) error {
		got = event
		return sentinel
	}
	// A detail that would also decode as an S3 event with a transcript key.
	raw := json.RawMessage(`{"source":"ttobak.analysis","detail-type":"TermsRequested","detail":{"ownerId":"o1","meetingId":"m1","runId":"r1",` +
		`"bucket":{"name":"b"},"object":{"key":"transcripts/m1.json"}}}`)
	if err := Handler(context.Background(), raw); !errors.Is(err, sentinel) {
		t.Fatalf("TermsRequested was not routed to the terms worker: %v", err)
	}
	if got != (model.TermsRequested{OwnerID: "o1", MeetingID: "m1", RunID: "r1"}) {
		t.Fatalf("decoded %+v", got)
	}
	processTermsRequested = func(context.Context, model.TermsRequested) error {
		t.Fatal("other sources must not reach the terms worker")
		return nil
	}
	if err := Handler(context.Background(), json.RawMessage(`{"source":"aws.s3","detail-type":"Object Created","detail":{"bucket":{"name":"b"},"object":{"key":"images/x.png"}}}`)); err != nil {
		t.Fatal(err)
	}
}

func TestHumanContentPreservedStillQueuesTerms(t *testing.T) {
	restore := enqueueTerms
	defer func() { enqueueTerms = restore }()
	var queued []string
	enqueueTerms = func(_ context.Context, owner, meeting, reason string) {
		queued = append(queued, owner+"/"+meeting+"/"+reason)
	}
	if !humanContentPreserved(context.Background(), fmt.Errorf("wrapped: %w", service.ErrSummaryHumanContentPreserved), "o1", "m1") {
		t.Fatal("preserved branch not detected")
	}
	if humanContentPreserved(context.Background(), errors.New("other"), "o1", "m1") || humanContentPreserved(context.Background(), nil, "o1", "m1") {
		t.Fatal("other outcomes are not the preserved branch")
	}
	if len(queued) != 1 || queued[0] != "o1/m1/human-content-preserved" {
		t.Fatalf("queued %v", queued)
	}
}

// KB bucket objects reach only the source mapper, and TermsSourceRequested
// only the source worker.
func TestHandlerRoutesTermsSources(t *testing.T) {
	restoreBucket, restoreRequest, restoreProcess := termsSourceBucket, requestTermsSource, processTermsSource
	defer func() {
		termsSourceBucket, requestTermsSource, processTermsSource = restoreBucket, restoreRequest, restoreProcess
	}()
	termsSourceBucket = "kb-bucket"
	var mapped []string
	requestTermsSource = func(_ context.Context, bucket, key string) error {
		mapped = append(mapped, bucket+"/"+key)
		return nil
	}
	var processed []model.TermsSourceRequested
	processTermsSource = func(_ context.Context, ev model.TermsSourceRequested) error {
		processed = append(processed, ev)
		return nil
	}
	events := []string{
		// The key reaches the mapper as delivered, not unescaped.
		`{"source":"aws.s3","detail-type":"Object Created","detail":{"bucket":{"name":"kb-bucket"},"object":{"key":"kb/u1/1_a+b%20c.md"}}}`,
		// A KB key that also looks like a transcript must not reach the transcript path.
		`{"source":"aws.s3","detail-type":"Object Created","detail":{"bucket":{"name":"kb-bucket"},"object":{"key":"transcripts/m1.json"}}}`,
		`{"source":"ttobak.analysis","detail-type":"TermsSourceRequested","detail":{"kind":"research","ref":"r1","ownerId":"u1","accountIds":["a1"]}}`,
		`{"source":"aws.s3","detail-type":"Object Created","detail":{"bucket":{"name":"assets"},"object":{"key":"images/x.png"}}}`,
	}
	for _, raw := range events {
		if err := Handler(context.Background(), json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if len(mapped) != 2 || mapped[0] != "kb-bucket/kb/u1/1_a+b%20c.md" || mapped[1] != "kb-bucket/transcripts/m1.json" {
		t.Fatalf("mapped %v", mapped)
	}
	if len(processed) != 1 || processed[0].Kind != "research" || processed[0].Ref != "r1" || processed[0].OwnerID != "u1" || len(processed[0].AccountIDs) != 1 {
		t.Fatalf("processed %+v", processed)
	}
}
