package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	chiadapter "github.com/awslabs/aws-lambda-go-api-proxy/chi"
	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

func TestDocumentReadingGatewayV1UsesAuthenticatedPages(t *testing.T) {
	repo := newMockHandlerAccountRepo()
	repo.documents["USER#owner"] = []model.AccountDocument{{
		PK: "USER#owner", DocID: "doc-1", Content: strings.Repeat("한😀\"\\\n", 10000),
	}}
	h := NewDocumentReadingHandler(service.NewDocumentReadingService(repo))
	router := chi.NewRouter()
	user := "owner"
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), middleware.UserIDKey, user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	router.Get("/api/documents/{docId}/reading", h.Get)
	adapter := chiadapter.New(router)
	request := events.APIGatewayProxyRequest{
		HTTPMethod: "GET", Path: "/api/documents/doc-1/reading",
		QueryStringParameters: map[string]string{"pageSize": "8000"},
	}
	response, err := adapter.ProxyWithContext(context.Background(), request)
	if err != nil || response.StatusCode != 200 || len(response.Body) > 10000 {
		t.Fatalf("bounded v1 response: status=%d bytes=%d err=%v", response.StatusCode, len(response.Body), err)
	}
	var page struct {
		Page struct {
			NextCursor string `json:"nextCursor"`
		} `json:"page"`
	}
	if err := json.Unmarshal([]byte(response.Body), &page); err != nil || page.Page.NextCursor == "" {
		t.Fatal("missing continuation")
	}
	request.QueryStringParameters["cursor"] = page.Page.NextCursor
	repo.documents["USER#owner"][0].Content = "edited"
	response, err = adapter.ProxyWithContext(context.Background(), request)
	if err != nil || response.StatusCode != 409 || !strings.Contains(response.Body, "STALE_CURSOR") {
		t.Fatalf("stale continuation: %d %s %v", response.StatusCode, response.Body, err)
	}
	user = ""
	response, err = adapter.ProxyWithContext(context.Background(), request)
	if err != nil || response.StatusCode != 401 {
		t.Fatalf("missing authentication: %d %v", response.StatusCode, err)
	}
}
