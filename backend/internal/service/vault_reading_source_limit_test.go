package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/repository"
)

type vaultBoundedSourceRepo struct {
	*vaultReadingRepo
	bounded *repository.DynamoDBRepository
}

func (r *vaultBoundedSourceRepo) ReadVaultTranscriptField(ctx context.Context, id, field, value string) (string, error) {
	return r.bounded.ReadVaultTranscriptField(ctx, id, field, value)
}

func TestVaultReadingBoundedSourcesThroughFallbackAndFreshness(t *testing.T) {
	_, repo := vaultReadingFixture()
	repo.docs, repo.attachments = nil, nil
	meeting := repo.meetings["reader|m"]
	meeting.AccountID, meeting.Notes, meeting.Content = "", "", ""
	meeting.SharedToAccount = false
	meeting.Title = "bounded source"
	meeting.TranscriptB = "s3://fixture/transcripts/m/transcriptB.txt"
	meeting.TranscriptSegments = "s3://fixture/transcripts/m/transcriptSegments.txt"
	const limit = 1024 * 1024
	selected := strings.Repeat("<", limit)
	// Valid JSON with many unusable legacy entries exercises allocation before
	// render fallback. The source remains within the same byte ceiling.
	segments := "[" + strings.Repeat("{},", (limit-2)/3-1) + "{}]"
	reads := map[string]int{}
	storage := s3.New(s3.Options{
		Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
		HTTPClient: noteSourceHTTPClient(func(req *http.Request) (*http.Response, error) {
			path := req.URL.Path
			reads[path]++
			var value string
			switch path {
			case "/transcripts/m/transcriptA.txt":
				value = strings.Repeat(" ", limit)
			case "/transcripts/m/transcriptB.txt":
				value = selected
			case "/transcripts/m/transcriptSegments.txt":
				value = segments
			default:
				t.Fatalf("unexpected storage request: %s", path)
			}
			return &http.Response{
				StatusCode: http.StatusOK, Header: http.Header{},
				Body: io.NopCloser(strings.NewReader(value)), ContentLength: int64(len(value)),
			}, nil
		}),
	})
	service := NewVaultReadingService(&vaultBoundedSourceRepo{
		vaultReadingRepo: repo,
		bounded:          repository.NewDynamoDBRepositoryWithS3(nil, "fixture", storage, "fixture"),
	})
	source, err := service.source(context.Background(), "reader", "meeting", "", "m")
	if err != nil {
		t.Fatal(err)
	}
	query := url.Values{"section": {"file"}, "fileId": {encodeVaultFile(source.token)}, "pageSize": {"1000"}}
	revision := ""
	for pageNumber := 0; pageNumber < 2; pageNumber++ {
		raw, err := service.Read(context.Background(), "reader", query)
		if err != nil || len(raw) > 14000 {
			t.Fatalf("bounded page failed: bytes=%d err=%v", len(raw), err)
		}
		var page struct {
			Markdown       string `json:"markdown"`
			SourceRevision string `json:"sourceRevision"`
			Fragment       struct {
				StartOffset, EndOffset, TotalCodePoints int
				Complete                                bool
			} `json:"fragment"`
			Page struct {
				Complete   bool
				NextCursor string
			} `json:"page"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			t.Fatal(err)
		}
		if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(page.SourceRevision) ||
			page.Fragment.StartOffset != pageNumber*1000 || page.Fragment.EndOffset != (pageNumber+1)*1000 ||
			len([]rune(page.Markdown)) != 1000 || page.Fragment.TotalCodePoints < limit ||
			page.Fragment.Complete || page.Page.Complete || page.Page.NextCursor == "" {
			t.Fatal("lost source size, page identity or continuation")
		}
		if !strings.Contains(page.Markdown, strings.Repeat("<", 100)) {
			t.Fatal("selected fallback text was omitted")
		}
		if pageNumber != 0 && revision != page.SourceRevision {
			t.Fatal("unchanged source revision changed between pages")
		}
		revision = page.SourceRevision
		query.Set("cursor", page.Page.NextCursor)
	}
	for _, path := range []string{"/transcripts/m/transcriptA.txt", "/transcripts/m/transcriptB.txt", "/transcripts/m/transcriptSegments.txt"} {
		if reads[path] != 4 {
			t.Fatalf("source was not independently rehydrated on both pages: %s count=%d", path, reads[path])
		}
	}
	selected += "<"
	query.Del("cursor")
	raw, err := service.Read(context.Background(), "reader", query)
	if !errors.Is(err, repository.ErrVaultSourceTooLarge) || len(raw) != 0 {
		t.Fatalf("oversized fallback returned partial data: bytes=%d err=%v", len(raw), err)
	}
}
