package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/service"
)

func TestVaultReadingConstructorKeepsManifestAndFileMetadataOnly(t *testing.T) {
	f := newReadingFixture(t)
	// B is deliberately oversized. Only selected A and the empty segment source
	// should be fetched by file rendering, after authorization.
	f.meeting.TranscriptSegments = "s3://reading-fixture/transcripts/m1/transcriptSegments.txt"
	f.objects["transcripts/m1/transcriptA.txt"] = "selected transcript"
	f.objects["transcripts/m1/transcriptSegments.txt"] = ""
	svc := service.NewVaultReadingService(f.repo)
	raw, err := svc.Read(context.Background(), "owner", url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.s3Keys) != 0 {
		t.Fatalf("manifest fetched spill objects before selecting a source: %v", f.s3Keys)
	}
	var envelope struct {
		DataJSON string `json:"dataJson"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		FileID string `json:"fileId"`
	}
	if err := json.Unmarshal([]byte(envelope.DataJSON), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("manifest did not retain its meeting: rows=%d err=%v", len(rows), err)
	}
	query := url.Values{"section": {"file"}, "fileId": {rows[0].FileID}}
	raw, err = svc.Read(context.Background(), "owner", query)
	if err != nil || !strings.Contains(string(raw), "selected transcript") {
		t.Fatalf("selected transcript missing: err=%v", err)
	}
	reads := map[string]int{}
	for _, key := range f.s3Keys {
		reads[key]++
	}
	if reads["transcripts/m1/transcriptA.txt"] != 2 ||
		reads["transcripts/m1/transcriptSegments.txt"] != 2 ||
		len(reads) != 2 {
		t.Fatalf("file bypassed selected-source/freshness reads: %v", reads)
	}

	f.s3Keys = nil
	f.objects["transcripts/m1/transcriptA.txt"] = strings.Repeat("X", 1024*1024+1)
	request := httptest.NewRequest(http.MethodGet, "/api/vault/reading?"+query.Encode(), nil)
	request = request.WithContext(context.WithValue(request.Context(), middleware.UserIDKey, "owner"))
	response := httptest.NewRecorder()
	NewVaultReadingHandler(svc).Get(response, request)
	var failure struct {
		Error struct{ Code, Message string }
	}
	if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusUnprocessableEntity || failure.Error.Code != "SOURCE_TOO_LARGE" ||
		!strings.Contains(failure.Error.Message, "1 MiB") ||
		response.Header().Get("Cache-Control") != "no-store" ||
		strings.Contains(response.Body.String(), strings.Repeat("X", 20)) {
		t.Fatalf("incorrect bounded response: %d %s", response.Code, response.Body.String())
	}
	if len(f.s3Keys) != 1 || f.s3Keys[0] != "transcripts/m1/transcriptA.txt" {
		t.Fatalf("oversized file fetched an unselected field: %v", f.s3Keys)
	}

	// Deriving a metadata view must not change the caller's ordinary repository.
	f.objects["transcripts/m1/transcriptA.txt"] = "normal A"
	f.objects["transcripts/m1/transcriptB.txt"] = "normal B"
	f.s3Keys = nil
	meeting, err := f.repo.GetMeeting(context.Background(), "owner", "m1")
	if err != nil || meeting.TranscriptA != "normal A" || meeting.TranscriptB != "normal B" ||
		len(f.s3Keys) != 3 {
		t.Fatalf("constructor changed ordinary repository hydration: reads=%v err=%v", f.s3Keys, err)
	}
}
