package middleware

import (
	"crypto/sha256"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

// RequestLog records registered route patterns, never request URLs. Public
// document tokens are bearer capabilities, including in unmatched or encoded
// paths. Headers, queries, remote addresses and bodies are also excluded.
func RequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		wrapped := chimiddleware.NewWrapResponseWriter(w, r.ProtoMajor)
		defer func() {
			route := "unmatched"
			if context := chi.RouteContext(r.Context()); context != nil {
				if pattern := context.RoutePattern(); pattern != "" {
					route = pattern
				}
			}
			status := wrapped.Status()
			if status == 0 {
				status = http.StatusOK
			}
			// chi accepts caller-supplied X-Request-ID values. Hash them
			// before logging so a capability in that header stays private.
			requestIDHash := "-"
			if requestID := chimiddleware.GetReqID(r.Context()); requestID != "" {
				digest := sha256.Sum256([]byte(requestID))
				requestIDHash = fmt.Sprintf("%x", digest[:16])
			}
			log.Printf("request completed method=%s route=%q status=%d bytes=%d duration_ms=%d request_id_hash=%s",
				loggedMethod(r.Method), route, status, wrapped.BytesWritten(),
				time.Since(start).Milliseconds(), requestIDHash)
		}()
		next.ServeHTTP(wrapped, r)
	})
}

func loggedMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodConnect, http.MethodOptions, http.MethodTrace:
		return method
	default:
		// An arbitrary extension method can itself contain a capability.
		return "OTHER"
	}
}
