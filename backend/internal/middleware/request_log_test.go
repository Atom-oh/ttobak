package middleware

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

func TestRequestLogNeverRecordsCapabilities(t *testing.T) {
	const capability = "SYNTHETIC_PUBLIC_CAPABILITY_73cdb72e"
	const privateText = "SYNTHETIC_PRIVATE_REQUEST_TEXT"
	var logs bytes.Buffer
	prior := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(prior) })

	router := chi.NewRouter()
	router.Use(chimiddleware.RequestID)
	router.Use(RequestLog)
	router.Use(Recovery)
	router.Get("/api/public/docs/{token}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	router.Route("/api/meetings/{meetingId}", func(r chi.Router) {
		r.Get("/notes", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("ok"))
		})
	})
	router.Get("/panic/{token}", func(w http.ResponseWriter, r *http.Request) {
		panic(privateText)
	})
	tests := []struct {
		name, method, path, route string
		status                    int
	}{
		{"public", "GET", "/api/public/docs/" + capability, "/api/public/docs/{token}", 204},
		{"query", "GET", "/api/public/docs/" + capability + "?token=" + privateText, "/api/public/docs/{token}", 204},
		{"encoded", "GET", "/api/public/docs/%53YNTHETIC_PUBLIC_CAPABILITY_73cdb72e", "/api/public/docs/{token}", 204},
		{"encoded-slash", "GET", "/api/public/docs/" + capability + "%2F" + privateText, "/api/public/docs/{token}", 204},
		{"unmatched", "GET", "/unknown/" + capability + "?token=" + privateText, "unmatched", 404},
		{"unmatched-child", "GET", "/api/public/docs/" + capability + "/missing", "unmatched", 404},
		{"nested", "GET", "/api/meetings/" + capability + "/notes", "/api/meetings/{meetingId}/notes", 200},
		{"method-not-allowed", "POST", "/api/public/docs/" + capability, "unmatched", 405},
		{"method-canary", capability, "/api/public/docs/" + capability, "unmatched", 405},
		{"panic", "GET", "/panic/" + capability, "/panic/{token}", 500},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			logs.Reset()
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(privateText))
			request.Header.Set("X-Request-ID", capability)
			request.Header.Set("Authorization", "Bearer "+capability)
			request.Header.Set("Referer", "https://example.invalid/"+capability)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status=%d, want %d", response.Code, test.status)
			}
			output := logs.String()
			for _, forbidden := range []string{capability, privateText, "%53YNTHETIC", "%2F", "example.invalid", "Bearer"} {
				if strings.Contains(output, forbidden) {
					t.Fatalf("request log exposed the %q canary", forbidden)
				}
			}
			if !strings.Contains(output, `route="`+test.route+`"`) ||
				!strings.Contains(output, "request_id_hash=") ||
				strings.Count(output, "request completed") != 1 {
				t.Fatalf("missing route/correlation or duplicate record: %s", output)
			}
			if test.name == "method-canary" && !strings.Contains(output, "method=OTHER") {
				t.Fatal("extension method was not redacted")
			}
			if test.name == "nested" && !strings.Contains(output, "bytes=2") {
				t.Fatal("response bytes were not recorded")
			}
		})
	}
}

func TestRequestLogEarlyRejectionDoesNotFallBackToURL(t *testing.T) {
	const capability = "SYNTHETIC_EARLY_REJECTION_CAPABILITY"
	var logs bytes.Buffer
	prior := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(prior) })
	router := chi.NewRouter()
	router.Use(RequestLog)
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		})
	})
	router.Get("/api/public/docs/{token}", func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("rejected request reached handler")
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest("GET", "/api/public/docs/"+capability+"?token="+capability, nil))
	if response.Code != http.StatusForbidden || strings.Contains(logs.String(), capability) ||
		!strings.Contains(logs.String(), `route="unmatched" status=403`) {
		t.Fatal("early rejection did not produce a redacted status record")
	}
}
