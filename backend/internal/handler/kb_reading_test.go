package handler

import (
	"context"
	"encoding/json"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/service"
)

func TestKBReadingHTTPRejectsUnauthenticatedAndMalformedOptionsWithoutProviderReads(t *testing.T) {
	client := s3.New(s3.Options{Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, HTTPClient: readingHTTP(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid request reached provider")
		return nil, nil
	})})
	handler := NewKBHandler(service.NewKBService(client, nil, "knowledge", "", ""))
	for _, test := range []struct {
		user, query, code string
		status            int
	}{
		{"", "", "UNAUTHORIZED", 401}, {"bad/user", "", "FORBIDDEN", 403},
		{"owner", "pageSize=0", "INVALID_ARGUMENT", 400}, {"owner", "pageSize=1&pageSize=2", "INVALID_ARGUMENT", 400},
		{"owner", "cursor=!!", "INVALID_CURSOR", 400}, {"owner", "cursor=%XX", "INVALID_ARGUMENT", 400},
	} {
		request := httptest.NewRequest("GET", "/api/kb/files/reading?"+test.query, nil)
		request = request.WithContext(context.WithValue(request.Context(), middleware.UserIDKey, test.user))
		response := httptest.NewRecorder()
		handler.ReadFiles(response, request)
		var result struct{ Error struct{ Code string } }
		if json.Unmarshal(response.Body.Bytes(), &result) != nil || response.Code != test.status || result.Error.Code != test.code || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(test, response.Code, response.Body.String())
		}
	}
}
