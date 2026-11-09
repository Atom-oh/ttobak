package middleware

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// gatedOriginSecret blocks each read until released; safe for concurrent use.
type gatedOriginSecret struct {
	raw     string
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
	maxWait time.Duration
}

func (g *gatedOriginSecret) GetSecretValue(ctx context.Context, input *secretsmanager.GetSecretValueInput, options ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	if g.calls.Add(1) == 1 && g.started != nil {
		close(g.started)
	}
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > g.maxWait {
		return nil, errors.New("lookup not bounded by the expected timeout")
	}
	if g.release != nil {
		select {
		case <-g.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &secretsmanager.GetSecretValueOutput{SecretString: aws.String(g.raw)}, nil
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })
	return &output
}

func TestOriginPrefetchFillsCacheButStillChecksEachRequest(t *testing.T) {
	client := &fakeOriginSecret{raw: marshalProof(proofDocument())}
	v := NewOriginVerifier("synthetic-secret-arn", client, false, strings.Repeat("b", 32))
	v.now = func() time.Time { return time.Unix(1010, 0) }
	v.Prefetch(context.Background(), time.Second)
	if client.calls != 1 || client.version != strings.Repeat("b", 32) {
		t.Fatalf("prefetch calls=%d version=%q", client.calls, client.version)
	}
	if !v.allows(context.Background(), http.Header{"X-Origin-Verify": {currentProof}}) {
		t.Fatal("prefetched proof rejected")
	}
	if v.allows(context.Background(), http.Header{"X-Origin-Verify": {"synthetic-wrong-proof-1234567890"}}) {
		t.Fatal("prefetch cached a decision instead of proofs")
	}
	if client.calls != 1 {
		t.Fatalf("request re-read a fresh prefetched cache: calls=%d", client.calls)
	}
	// A fresh cache makes a repeated prefetch a no-op.
	v.Prefetch(context.Background(), time.Second)
	if client.calls != 1 {
		t.Fatalf("prefetch re-read a fresh cache: calls=%d", client.calls)
	}
}

func TestOriginPrefetchFailureLeavesRequestPathUnchanged(t *testing.T) {
	for _, scenario := range []struct {
		category string
		client   *fakeOriginSecret
	}{
		{"secret_unavailable", &fakeOriginSecret{err: errors.New("private provider message")}},
		{"secret_invalid", &fakeOriginSecret{raw: "private malformed document"}},
	} {
		t.Run(scenario.category, func(t *testing.T) {
			output := captureLog(t)
			v := NewOriginVerifier("synthetic-secret-arn", scenario.client, false, strings.Repeat("b", 32))
			v.now = func() time.Time { return time.Unix(1010, 0) }
			v.Prefetch(context.Background(), time.Second)
			logged := output.String()
			if !strings.Contains(logged, "http_origin_prefetch_failed category="+scenario.category) ||
				strings.Contains(logged, "http_origin_rejected") || strings.Contains(logged, "private") ||
				strings.Contains(logged, "synthetic-secret-arn") {
				t.Fatalf("unexpected prefetch diagnostics: %q", logged)
			}
			if _, ok := v.cached(); ok {
				t.Fatal("failed prefetch populated the cache")
			}
			// The request performs its own lookup and still fails closed.
			if v.allows(context.Background(), http.Header{"X-Origin-Verify": {currentProof}}) {
				t.Fatal("failure was authorized")
			}
			if scenario.client.calls != 2 {
				t.Fatalf("calls=%d, want prefetch + request lookup", scenario.client.calls)
			}
			// Once the secret is readable the next request succeeds as before.
			scenario.client.err, scenario.client.raw = nil, marshalProof(proofDocument())
			if !v.allows(context.Background(), http.Header{"X-Origin-Verify": {currentProof}}) {
				t.Fatal("request path did not recover after a failed prefetch")
			}
		})
	}
}

func TestOriginPrefetchSkipsInvalidConfiguration(t *testing.T) {
	output := captureLog(t)
	for _, v := range []*OriginVerifier{
		nil,
		NewOriginVerifier("", &fakeOriginSecret{}, true, strings.Repeat("b", 32)),
		NewOriginVerifier("synthetic-secret-arn", nil, false, strings.Repeat("b", 32)),
		NewOriginVerifier("synthetic-secret-arn", &fakeOriginSecret{}, false, ""),
	} {
		v.Prefetch(context.Background(), time.Second)
		if v != nil {
			if client, ok := v.client.(*fakeOriginSecret); ok && client.calls != 0 {
				t.Fatal("prefetch read a secret under invalid configuration")
			}
		}
	}
	if output.Len() != 0 {
		t.Fatalf("skipped prefetch logged: %q", output.String())
	}
}

func TestOriginPrefetchSkipsWhenLookupInFlight(t *testing.T) {
	client := &fakeOriginSecret{raw: marshalProof(proofDocument())}
	v := NewOriginVerifier("synthetic-secret-arn", client, false, strings.Repeat("b", 32))
	v.lookup <- struct{}{} // a request holds the lookup slot
	done := make(chan struct{})
	go func() {
		v.Prefetch(context.Background(), time.Second)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("prefetch blocked behind a request lookup")
	}
	<-v.lookup
	if client.calls != 0 {
		t.Fatal("prefetch read the secret while a request lookup was running")
	}
}

// A request arriving during the prefetch waits for it and uses its result:
// one Secrets Manager read in total.
func TestOriginRequestDuringPrefetchUsesPrefetchedProofs(t *testing.T) {
	client := &gatedOriginSecret{raw: marshalProof(proofDocument()), started: make(chan struct{}),
		release: make(chan struct{}), maxWait: time.Second}
	v := NewOriginVerifier("synthetic-secret-arn", client, false, strings.Repeat("b", 32))
	v.now = time.Now
	prefetched := make(chan struct{})
	go func() {
		v.Prefetch(context.Background(), time.Second)
		close(prefetched)
	}()
	<-client.started
	allowed := make(chan bool, 1)
	go func() {
		allowed <- v.allows(context.Background(), http.Header{"X-Origin-Verify": {currentProof}})
	}()
	time.Sleep(20 * time.Millisecond)
	close(client.release)
	<-prefetched
	if !<-allowed {
		t.Fatal("request during prefetch was rejected")
	}
	if got := client.calls.Load(); got != 1 {
		t.Fatalf("secret reads=%d, want 1", got)
	}
}

// A hanging prefetch is cut off by its own timeout, leaving the waiting
// request time for its own lookup within the request path's bound.
func TestOriginHangingPrefetchIsBounded(t *testing.T) {
	client := &gatedOriginSecret{raw: marshalProof(proofDocument()), started: make(chan struct{}),
		release: make(chan struct{}), maxWait: 3 * time.Second}
	v := NewOriginVerifier("synthetic-secret-arn", client, false, strings.Repeat("b", 32))
	captureLog(t)
	go v.Prefetch(context.Background(), 50*time.Millisecond)
	<-client.started
	// Let the request's own read through; only the prefetch's read hangs.
	go func() {
		for client.calls.Load() < 2 {
			time.Sleep(time.Millisecond)
		}
		close(client.release)
	}()
	start := time.Now()
	if !v.allows(context.Background(), http.Header{"X-Origin-Verify": {currentProof}}) {
		t.Fatal("request after a timed-out prefetch was rejected")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("request waited %v behind a hanging prefetch", elapsed)
	}
}
