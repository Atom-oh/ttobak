package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The warm-up leaves a pooled connection that the next call on the same
// client reuses instead of dialing again.
func TestWarmHTTPConnectionLeavesReusableConnection(t *testing.T) {
	var dials, requests atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte("healthy"))
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			dials.Add(1)
		}
	}
	server.Start()
	defer server.Close()
	client := &http.Client{Transport: &http.Transport{}}
	defer client.CloseIdleConnections()

	warmHTTPConnection(client, server.URL+"/", time.Second)
	resp, err := client.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if requests.Load() != 2 || dials.Load() != 1 {
		t.Fatalf("requests=%d dials=%d, want 2 requests over 1 connection", requests.Load(), dials.Load())
	}
}

// Errors and hangs are swallowed and bounded by the timeout.
func TestWarmHTTPConnectionIsBestEffortAndBounded(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer close(release)

	start := time.Now()
	warmHTTPConnection(http.DefaultClient, server.URL+"/", 50*time.Millisecond)
	warmHTTPConnection(http.DefaultClient, "http://127.0.0.1:1/", 50*time.Millisecond)
	warmHTTPConnection(http.DefaultClient, "://bad-url", 50*time.Millisecond)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("warm-up ran %v past its timeout", elapsed)
	}
}

func TestProvisionedInit(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"provisioned-concurrency", true},
		{"on-demand", false},
		{"snap-start", false},
		{"", false},
	} {
		got := provisionedInit(func(key string) string {
			if key == "AWS_LAMBDA_INITIALIZATION_TYPE" {
				return tc.value
			}
			return ""
		})
		if got != tc.want {
			t.Errorf("provisionedInit(%q) = %v, want %v", tc.value, got, tc.want)
		}
	}
}

func TestRunWarmupsWaitsForEveryWarmup(t *testing.T) {
	var mu sync.Mutex
	done := 0
	release := make(chan struct{})
	warm := func() {
		<-release
		mu.Lock()
		done++
		mu.Unlock()
	}
	wait := runWarmups(warm, warm, warm)
	finished := make(chan struct{})
	go func() { wait(); close(finished) }()
	select {
	case <-finished:
		t.Fatal("wait returned before the warm-ups finished")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("wait did not return after the warm-ups finished")
	}
	mu.Lock()
	defer mu.Unlock()
	if done != 3 {
		t.Fatalf("finished warm-ups = %d, want 3", done)
	}
}
