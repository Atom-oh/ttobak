package main

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/ttobak/backend/internal/middleware"
)

// coldStartPrefetchTimeout bounds each background cold-start warm-up. A
// request that needs a value while its warm-up is in flight waits at most
// this long before doing its own bounded lookup, so it stays well below the
// request-path bounds (3 s origin secret, 5 s JWKS).
const coldStartPrefetchTimeout = time.Second

// startColdStartWarmups runs, in parallel goroutines off the init path, the
// lookups this instance's first request would otherwise do serially: the
// origin proof secret, the Cognito JWKS and a pooled DynamoDB connection.
// Each is bounded and best effort; a failure leaves the request path's own
// on-demand lookup unchanged.
func startColdStartWarmups(origin *middleware.OriginVerifier, dynamoClient *dynamodb.Client, region string) {
	go origin.Prefetch(context.Background(), coldStartPrefetchTimeout)
	go middleware.PrefetchJWKS(coldStartPrefetchTimeout)
	// The regional endpoint the client resolves to without account-ID
	// routing (Lambda sets no AWS_ACCOUNT_ID). If it ever differed, the only
	// cost would be one unused idle connection.
	go warmHTTPConnection(dynamoClient.Options().HTTPClient, "https://dynamodb."+region+".amazonaws.com/", coldStartPrefetchTimeout)
}

// httpDoer is the subset of aws.HTTPClient used to open a pooled connection.
type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// warmHTTPConnection sends one unsigned GET through client so its pool holds
// an open TCP+TLS connection to url's host when the first real call needs
// one. Unsigned, so no AWS API operation or IAM permission is involved; the
// endpoint's health response is discarded. Best effort: any error is ignored
// because a later call simply dials its own connection as it does today.
func warmHTTPConnection(client httpDoer, url string, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	// Drain a bounded amount so the connection can return to the idle pool.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
}
