package main

import (
	"net"
	"net/http"
	"net/http/httptest"
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
