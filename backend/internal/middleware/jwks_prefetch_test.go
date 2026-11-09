package middleware

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// withJWKSServer points the JWKS fetch at a local server, with an empty cache,
// and restores the package state afterwards.
func withJWKSServer(t *testing.T, poolID string, handler http.HandlerFunc) *atomic.Int32 {
	t.Helper()
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		handler(w, r)
	}))
	previousURL, previousPool := jwksURL, cognitoUserPoolID
	jwksURL = func() string { return server.URL + "/jwks.json" }
	cognitoUserPoolID = poolID
	resetJWKSCache := func() {
		jwksCache.Lock()
		jwksCache.keys, jwksCache.fetchedAt = nil, time.Time{}
		jwksCache.Unlock()
	}
	resetJWKSCache()
	t.Cleanup(func() {
		server.Close()
		jwksURL, cognitoUserPoolID = previousURL, previousPool
		resetJWKSCache()
	})
	return &hits
}

func jwksDocument(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	doc := jwksResponse{Keys: []jwkKey{{
		Kid: "kid-1", Kty: "RSA", Alg: "RS256", Use: "sig",
		N: base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		E: base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
	}}}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestPrefetchJWKSFillsCacheForRequests(t *testing.T) {
	doc := jwksDocument(t)
	hits := withJWKSServer(t, "pool-1", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(doc) })
	PrefetchJWKS(time.Second)
	keys, err := getJWKSKeys()
	if err != nil || keys["kid-1"] == nil {
		t.Fatalf("keys=%v err=%v", keys, err)
	}
	if hits.Load() != 1 {
		t.Fatalf("JWKS fetches=%d, want 1 (request reused the prefetch)", hits.Load())
	}
}

func TestPrefetchJWKSFailureLeavesOnDemandFetch(t *testing.T) {
	doc := jwksDocument(t)
	var ready atomic.Bool
	hits := withJWKSServer(t, "pool-1", func(w http.ResponseWriter, _ *http.Request) {
		if !ready.Load() {
			_, _ = w.Write([]byte("not json"))
			return
		}
		_, _ = w.Write(doc)
	})
	output := captureLog(t)
	PrefetchJWKS(time.Second)
	if !strings.Contains(output.String(), "JWKS prefetch failed") {
		t.Fatalf("prefetch failure not logged: %q", output.String())
	}
	ready.Store(true)
	keys, err := getJWKSKeys()
	if err != nil || keys["kid-1"] == nil {
		t.Fatalf("request fetch after failed prefetch: keys=%v err=%v", keys, err)
	}
	if hits.Load() != 2 {
		t.Fatalf("JWKS fetches=%d, want 2", hits.Load())
	}
}

func TestPrefetchJWKSTimeoutIsBounded(t *testing.T) {
	release := make(chan struct{})
	withJWKSServer(t, "pool-1", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	defer close(release)
	captureLog(t)
	start := time.Now()
	PrefetchJWKS(50 * time.Millisecond)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("prefetch ran %v past its timeout", elapsed)
	}
}

func TestPrefetchJWKSSkipsWithoutPool(t *testing.T) {
	hits := withJWKSServer(t, "", func(w http.ResponseWriter, _ *http.Request) {})
	PrefetchJWKS(time.Second)
	if hits.Load() != 0 {
		t.Fatal("prefetch fetched JWKS without a configured pool")
	}
}
