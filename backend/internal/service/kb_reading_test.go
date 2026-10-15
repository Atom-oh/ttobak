package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type kbReadingObject struct {
	size     int64
	etag     string
	modified time.Time
}
type kbReadingFixture struct {
	objects        map[string]kbReadingObject
	lists, heads   int
	foreign        bool
	failList       bool
	afterFirstHead func(string)
}

func newKBReadingFixture(t *testing.T) (*KBService, *kbReadingFixture) {
	t.Helper()
	f := &kbReadingFixture{objects: map[string]kbReadingObject{}}
	client := s3.New(s3.Options{
		Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
		HTTPClient: indexProviderHTTP(func(r *http.Request) (*http.Response, error) {
			if r.Method == http.MethodHead {
				f.heads++
				key := strings.TrimPrefix(r.URL.Path, "/")
				obj, ok := f.objects[key]
				if !ok {
					return indexHTTPResponse(404, "", nil), nil
				}
				if f.afterFirstHead != nil {
					callback := f.afterFirstHead
					f.afterFirstHead = nil
					callback(key)
				}
				out := indexHTTPResponse(200, "", map[string]string{
					"ETag": obj.etag, "Content-Length": strconv.FormatInt(obj.size, 10),
					"Last-Modified": obj.modified.UTC().Format(http.TimeFormat),
				})
				out.ContentLength = obj.size
				return out, nil
			}
			q := r.URL.Query()
			if r.Method != http.MethodGet || q.Get("list-type") != "2" || q.Get("prefix") != "kb/owner/" {
				t.Fatal("KB reading attempted an object download or incorrect prefix")
			}
			f.lists++
			if f.failList {
				return indexHTTPResponse(403, `<Error><Code>AccessDenied</Code></Error>`, nil), nil
			}
			limit, err := strconv.Atoi(q.Get("max-keys"))
			if err != nil || limit < 1 || limit > 50 || q.Get("continuation-token") != "" {
				t.Fatal("unbounded or unexpected provider pagination")
			}
			keys := []string{}
			for key := range f.objects {
				if strings.HasPrefix(key, q.Get("prefix")) && key > q.Get("start-after") {
					keys = append(keys, key)
				}
			}
			sort.Strings(keys)
			if f.foreign {
				keys = []string{"kb/another-user/private.pdf"}
			}
			more := len(keys) > limit
			if more {
				keys = keys[:limit]
			}
			var body strings.Builder
			body.WriteString(`<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
			fmt.Fprintf(&body, "<IsTruncated>%t</IsTruncated>", more)
			for _, key := range keys {
				fmt.Fprintf(&body, "<Contents><Key>%s</Key></Contents>", html.EscapeString(key))
			}
			body.WriteString("</ListBucketResult>")
			return indexHTTPResponse(200, body.String(), nil), nil
		}),
	})
	return NewKBService(client, nil, "knowledge", "", ""), f
}

func kbFixtureObject(i int) kbReadingObject {
	return kbReadingObject{size: int64(i + 1), etag: fmt.Sprintf(`"etag-%d"`, i),
		modified: time.Date(2026, 10, 1, 1, 2, 3, 0, time.UTC)}
}

func decodeKBPage(t *testing.T, body []byte) map[string]interface{} {
	t.Helper()
	if len(body) > 10000 {
		t.Fatal("KB API page exceeds encoded budget")
	}
	envelope, err := json.Marshal(map[string]interface{}{
		"content":           []map[string]string{{"type": "text", "text": string(body)}},
		"structuredContent": json.RawMessage(body),
	})
	if err != nil || len(envelope) > 32000 {
		t.Fatal("KB page cannot fit a structured MCP result")
	}
	var result map[string]interface{}
	if json.Unmarshal(body, &result) != nil {
		t.Fatal("invalid KB page")
	}
	return result
}

func TestKBReadingRestoresLargeListingsWithOneBoundedListPerPage(t *testing.T) {
	s, f := newKBReadingFixture(t)
	for i := 0; i < 75; i++ {
		key := fmt.Sprintf("kb/owner/%03d_%s.pdf", i, strings.Repeat("한<", 80))
		f.objects[key] = kbFixtureObject(i)
	}
	q, seen := url.Values{"pageSize": {"50"}}, map[string]bool{}
	for pages := 0; ; pages++ {
		before := f.lists
		body, err := s.ReadFiles(context.Background(), "owner", q)
		if err != nil {
			t.Fatal(err)
		}
		if f.lists != before+1 {
			t.Fatal("one API request consumed multiple provider pages")
		}
		page := decodeKBPage(t, body)
		for _, raw := range page["files"].([]interface{}) {
			file := raw.(map[string]interface{})
			id := file["fileId"].(string)
			if seen[id] || file["fileType"] != "application/pdf" {
				t.Fatal("duplicate ID or incorrect file metadata")
			}
			seen[id] = true
		}
		next := page["page"].(map[string]interface{})
		if next["complete"] == true {
			break
		}
		if pages > 100 {
			t.Fatal("KB cursor did not advance")
		}
		q.Set("cursor", next["nextCursor"].(string))
	}
	if len(seen) != 75 {
		t.Fatalf("lost KB files: %d", len(seen))
	}
}

func TestKBReadingBindsCallerAndCurrentAnchorAndNeverReturnsForeignPrefix(t *testing.T) {
	s, f := newKBReadingFixture(t)
	f.objects["kb/owner/a.pdf"], f.objects["kb/owner/b.pdf"] = kbFixtureObject(1), kbFixtureObject(2)
	body, err := s.ReadFiles(context.Background(), "owner", url.Values{"pageSize": {"1"}})
	if err != nil {
		t.Fatal(err)
	}
	value := decodeKBPage(t, body)
	q := url.Values{"cursor": {value["page"].(map[string]interface{})["nextCursor"].(string)}}
	if _, err := s.ReadFiles(context.Background(), "other", q); !errors.Is(err, ErrStaleReadingCursor) {
		t.Fatal("cross-user cursor accepted", err)
	}
	f.objects["kb/owner/a.pdf"] = kbFixtureObject(99)
	if _, err := s.ReadFiles(context.Background(), "owner", q); !errors.Is(err, ErrStaleReadingCursor) {
		t.Fatal("changed anchor accepted", err)
	}
	delete(f.objects, "kb/owner/a.pdf")
	if _, err := s.ReadFiles(context.Background(), "owner", q); !errors.Is(err, ErrStaleReadingCursor) {
		t.Fatal("deleted anchor accepted", err)
	}
	f.foreign = true
	before := f.heads
	if body, err := s.ReadFiles(context.Background(), "owner", nil); !errors.Is(err, ErrReadingData) || body != nil || f.heads != before {
		t.Fatal("foreign-prefix object was read or returned")
	}
}

func TestKBReadingRejectsChangedObjectsBeforePublicationAndProviderFailures(t *testing.T) {
	s, f := newKBReadingFixture(t)
	f.objects["kb/owner/a.md"] = kbFixtureObject(1)
	f.afterFirstHead = func(key string) { f.objects[key] = kbFixtureObject(2) }
	if body, err := s.ReadFiles(context.Background(), "owner", nil); !errors.Is(err, ErrStaleReadingCursor) || body != nil {
		t.Fatal("changed metadata published as current")
	}
	f.failList = true
	if body, err := s.ReadFiles(context.Background(), "owner", nil); err == nil || body != nil {
		t.Fatal("failed listing reported partial success")
	}
}

func TestKBReadingPreservesLongFileIDsAndBoundsEncodedCursors(t *testing.T) {
	s, f := newKBReadingFixture(t)
	key := "kb/owner/" + strings.Repeat("<", 1011) + ".pdf"
	if len(key) != 1024 {
		t.Fatal("fixture is not a maximum-sized S3 key")
	}
	f.objects[key] = kbFixtureObject(1)
	body, err := s.ReadFiles(context.Background(), "owner", nil)
	if err != nil {
		t.Fatal(err)
	}
	value := decodeKBPage(t, body)
	file := value["files"].([]interface{})[0].(map[string]interface{})
	if file["fileId"] != kbFileID(key) || len(file["metadataTruncated"].([]interface{})) == 0 {
		t.Fatal("file ID truncated or shortened display name not marked")
	}
	cursor, _ := json.Marshal(kbReadingCursor{1, strings.Repeat("a", 64),
		base64.RawURLEncoding.EncodeToString([]byte(key)), strings.Repeat("b", 64)})
	if len(base64.RawURLEncoding.EncodeToString(cursor)) > 2048 {
		t.Fatal("maximum legitimate object key exceeds cursor bound")
	}
}
