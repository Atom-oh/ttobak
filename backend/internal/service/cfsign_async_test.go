package service

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testCloudFrontSigner(t *testing.T) *CloudFrontSigner {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return newCloudFrontSignerWithKey("https://ttobak.example.com/media", "KTESTKEYPAIRID", key)
}

func testUploadService(t *testing.T) *UploadService {
	t.Helper()
	return &UploadService{presignClient: newTestPresignClient(t), bucketName: "test-bucket"}
}

func isCloudFrontURL(u string) bool {
	return strings.HasPrefix(u, "https://ttobak.example.com/media/")
}

func isS3PresignURL(u string) bool {
	return strings.Contains(u, "test-bucket") && strings.Contains(u, "X-Amz-Signature")
}

func waitClosed(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("background signer load did not finish")
	}
}

// Load finished before the first request: the request signs with CloudFront
// and makes no further SSM call.
func TestAsyncSignerReadyBeforeRequest(t *testing.T) {
	svc := testUploadService(t)
	signer := testCloudFrontSigner(t)
	var calls atomic.Int32
	done := svc.StartCloudFrontSignerLoad(func(ctx context.Context) (*CloudFrontSigner, error) {
		calls.Add(1)
		if _, ok := ctx.Deadline(); !ok {
			return nil, errors.New("load is not bounded")
		}
		return signer, nil
	}, time.Second)
	waitClosed(t, done)

	got, err := svc.GeneratePresignedDownloadURL(t.Context(), "audio/u1/m1/a.webm")
	if err != nil {
		t.Fatal(err)
	}
	if !isCloudFrontURL(got) {
		t.Fatalf("expected CloudFront URL, got %s", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("reload calls = %d, want 1", calls.Load())
	}
}

// A request that arrives while the load is in flight waits for it and gets
// the same CloudFront URL a synchronous cold-start load would have produced.
func TestAsyncSignerInFlightRequestWaitsForLoad(t *testing.T) {
	svc := testUploadService(t)
	signer := testCloudFrontSigner(t)
	release := make(chan struct{})
	started := make(chan struct{})
	var calls atomic.Int32
	done := svc.StartCloudFrontSignerLoad(func(ctx context.Context) (*CloudFrontSigner, error) {
		calls.Add(1)
		close(started)
		<-release
		return signer, nil
	}, 5*time.Second)
	<-started

	result := make(chan string, 1)
	go func() {
		got, err := svc.GeneratePresignedDownloadURL(context.Background(), "audio/u1/m1/a.webm")
		if err != nil {
			got = "error: " + err.Error()
		}
		result <- got
	}()
	select {
	case got := <-result:
		t.Fatalf("request did not wait for the in-flight load: %s", got)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	waitClosed(t, done)
	if got := <-result; !isCloudFrontURL(got) {
		t.Fatalf("expected CloudFront URL after waiting, got %s", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("reload calls = %d, want 1 (no concurrent lazy retry)", calls.Load())
	}
}

// A request whose context ends before the load finishes stops waiting and
// gets no signer (the S3-presign branch, as before), without starting a
// second SSM load; a later request uses the signer once it is loaded.
func TestAsyncSignerNotReadyRequestFallsBack(t *testing.T) {
	svc := testUploadService(t)
	signer := testCloudFrontSigner(t)
	release := make(chan struct{})
	var calls atomic.Int32
	done := svc.StartCloudFrontSignerLoad(func(ctx context.Context) (*CloudFrontSigner, error) {
		calls.Add(1)
		<-release
		return signer, nil
	}, 5*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if got := svc.cloudFrontSigner(ctx); got != nil {
		t.Fatal("signer returned before the load finished")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("request waited %v past its own deadline", elapsed)
	}

	close(release)
	waitClosed(t, done)
	got, err := svc.GeneratePresignedDownloadURL(t.Context(), "audio/u1/m1/a.webm")
	if err != nil {
		t.Fatal(err)
	}
	if !isCloudFrontURL(got) {
		t.Fatalf("expected CloudFront URL once loaded, got %s", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("reload calls = %d, want 1", calls.Load())
	}
}

// A failed load falls back to S3 presign, and the first request retries
// immediately through the lazy path (as after a failed synchronous load),
// then waits out the retry interval after a second failure.
func TestAsyncSignerFailureFallsBackAndRetriesLazily(t *testing.T) {
	svc := testUploadService(t)
	signer := testCloudFrontSigner(t)
	var calls atomic.Int32
	var succeed atomic.Bool
	done := svc.StartCloudFrontSignerLoad(func(ctx context.Context) (*CloudFrontSigner, error) {
		calls.Add(1)
		if succeed.Load() {
			return signer, nil
		}
		return nil, errors.New("ssm unavailable")
	}, time.Second)
	waitClosed(t, done)

	got, err := svc.GeneratePresignedDownloadURL(t.Context(), "audio/u1/m1/a.webm")
	if err != nil {
		t.Fatal(err)
	}
	if !isS3PresignURL(got) {
		t.Fatalf("expected S3 presign fallback, got %s", got)
	}
	if calls.Load() != 2 {
		t.Fatalf("reload calls = %d, want 2 (background + first lazy retry)", calls.Load())
	}

	// Within the retry interval no further SSM call is made.
	succeed.Store(true)
	if got, _ := svc.GeneratePresignedDownloadURL(t.Context(), "audio/u1/m1/a.webm"); !isS3PresignURL(got) {
		t.Fatalf("expected S3 presign within retry interval, got %s", got)
	}
	if calls.Load() != 2 {
		t.Fatalf("reload calls = %d, want 2 within retry interval", calls.Load())
	}

	// After the interval the lazy retry recovers the signer.
	svc.cfSignerMu.Lock()
	svc.cfSignerRetryAt = time.Now().Add(-time.Second)
	svc.cfSignerMu.Unlock()
	if got, _ := svc.GeneratePresignedDownloadURL(t.Context(), "audio/u1/m1/a.webm"); !isCloudFrontURL(got) {
		t.Fatalf("expected CloudFront URL after lazy retry, got %s", got)
	}
}

// The background load's timeout bounds a hanging SSM call, and so bounds how
// long a waiting request can block.
func TestAsyncSignerLoadTimeoutBoundsWaitingRequest(t *testing.T) {
	svc := testUploadService(t)
	var calls atomic.Int32
	done := svc.StartCloudFrontSignerLoad(func(ctx context.Context) (*CloudFrontSigner, error) {
		if calls.Add(1) == 1 {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return nil, errors.New("still unavailable")
	}, 50*time.Millisecond)

	start := time.Now()
	got, err := svc.GeneratePresignedDownloadURL(context.Background(), "audio/u1/m1/a.webm")
	if err != nil {
		t.Fatal(err)
	}
	if !isS3PresignURL(got) {
		t.Fatalf("expected S3 presign fallback, got %s", got)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("request blocked %v, want it bounded by the load timeout", elapsed)
	}
	waitClosed(t, done)
}
