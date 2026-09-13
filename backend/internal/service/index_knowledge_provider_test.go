package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func TestKnowledgeOriginalDocumentStatusReadDoesNotAllowOriginalMutation(t *testing.T) {
	keys := []string{"kb/owner/file.pdf", "shared/reference/file.docx"}
	calls := 0
	p := indexHTTPProvider(t, func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != http.MethodPost || !strings.HasSuffix(req.URL.Path, "/documents/getDocuments") {
			t.Fatalf("original status lookup performed a different operation: %s %s", req.Method, req.URL)
		}
		var input struct {
			Documents []struct {
				Type string `json:"dataSourceType"`
				S3   struct {
					URI string `json:"uri"`
				} `json:"s3"`
			} `json:"documentIdentifiers"`
		}
		if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if len(input.Documents) != len(keys) {
			t.Fatal("original document identifiers missing")
		}
		details := []map[string]interface{}{}
		for i, document := range input.Documents {
			if document.Type != "S3" || document.S3.URI != "s3://knowledge/"+keys[i] {
				t.Fatal("original status lookup changed bucket/key")
			}
			details = append(details, map[string]interface{}{
				"knowledgeBaseId": "KB12345678", "dataSourceId": "DS12345678", "status": "NOT_FOUND",
				"identifier": map[string]interface{}{"dataSourceType": "S3", "s3": map[string]string{"uri": document.S3.URI}},
			})
		}
		out, _ := json.Marshal(map[string]interface{}{"documentDetails": details})
		return indexHTTPResponse(200, string(out), map[string]string{"Content-Type": "application/json"}), nil
	})
	statuses, err := p.Documents(context.Background(), keys)
	if err != nil || statuses[keys[0]] != "NOT_FOUND" || statuses[keys[1]] != "NOT_FOUND" {
		t.Fatalf("original removal proof unavailable: %v %v", statuses, err)
	}
	for _, key := range keys {
		if err := p.Put(context.Background(), key, "", nil); !errors.Is(err, ErrIndexInvalid) {
			t.Fatal("read-only original support widened publication permissions")
		}
		if err := p.Delete(context.Background(), key); !errors.Is(err, ErrIndexInvalid) {
			t.Fatal("read-only original support widened deletion permissions")
		}
	}
	for _, key := range []string{"docs/owner/file.pdf", "kb/owner/../file.pdf", "shared/../file.pdf", "kb//file.pdf"} {
		if _, err := p.Documents(context.Background(), []string{key}); !errors.Is(err, ErrIndexInvalid) {
			t.Fatalf("unscoped original status lookup accepted %s", key)
		}
	}
	if calls != 1 {
		t.Fatal("rejected original mutations or invalid lookups reached AWS")
	}
}

func TestKnowledgePinnedOldVersionIsRejectedAfterCurrentHeadChanges(t *testing.T) {
	current := "v1"
	heads, gets := 0, 0
	provider := indexHTTPProvider(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/knowledge/kb/owner/file.pdf" {
			t.Fatal("source request escaped")
		}
		switch req.Method {
		case http.MethodHead:
			heads++
			response := indexHTTPResponse(200, "", map[string]string{
				"ETag": `"` + current + `"`, "X-Amz-Version-Id": current,
				"Content-Length": "4", "Content-Type": "application/pdf",
			})
			response.ContentLength = 4
			return response, nil
		case http.MethodGet:
			gets++
			if req.Header.Get("If-Match") != `"v1"` || req.URL.Query().Get("versionId") != "v1" {
				t.Fatal("old version was not pinned")
			}
			current = "v2"
			// Versioned S3 may return v1 successfully even though v2 is now current.
			return indexHTTPResponse(200, "old!", map[string]string{
				"ETag": `"v1"`, "X-Amz-Version-Id": "v1", "Content-Type": "application/pdf",
			}), nil
		default:
			t.Fatal("stale source reached publication")
			return nil, nil
		}
	})
	service, _, _, _, _ := newIndexTest()
	service.objects = provider
	key, _ := model.KnowledgeIndexResource("kb/owner/file.pdf")
	if _, err := service.ReadSource(context.Background(), key, true); !errors.Is(err, ErrIndexChanged) {
		t.Fatalf("old readable version was claimed current: %v", err)
	}
	if heads != 2 || gets != 1 {
		t.Fatalf("freshness protocol incomplete: heads=%d gets=%d", heads, gets)
	}
}

func TestKnowledgeProviderReadsConfiguredBucketWithVersionAndType(t *testing.T) {
	for _, key := range []string{"kb/owner/file.pdf", "shared/reference/file.docx"} {
		t.Run(key, func(t *testing.T) {
			calls := 0
			p := indexHTTPProvider(t, func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Path != "/knowledge/"+key {
					t.Fatalf("wrong source bucket/key: %s", req.URL)
				}
				headers := map[string]string{"ETag": `"e1"`, "X-Amz-Version-Id": "version+/=",
					"Content-Type": "application/custom-document", "Content-Length": "4"}
				if req.Method == http.MethodHead {
					response := indexHTTPResponse(200, "", headers)
					response.ContentLength = 4
					return response, nil
				}
				if req.Method != http.MethodGet || req.Header.Get("If-Match") != `"e1"` ||
					req.URL.Query().Get("versionId") != "version+/=" {
					t.Fatalf("source read was not pinned: %s %v", req.URL, req.Header)
				}
				return indexHTTPResponse(200, "data", headers), nil
			})
			object, err := p.HeadKnowledge(context.Background(), key)
			if err != nil {
				t.Fatal(err)
			}
			body, err := p.ReadKnowledge(context.Background(), object)
			if err != nil || string(body) != "data" || object.ContentType != "application/custom-document" ||
				p.KnowledgeBucket() != "knowledge" || calls != 2 {
				t.Fatalf("source bytes/type/bucket lost: %+v %q %v", object, body, err)
			}
		})
	}
}

func TestKnowledgeProviderScopesWritesCleanupAndCatalogPagination(t *testing.T) {
	listCalls, puts, deletes := 0, 0, 0
	resource, _ := model.KnowledgeIndexResource("kb/owner/file.pdf")
	objectKey := resource.Prefix() + strings.Repeat("a", 64) + "/00000000-0000-4000-8000-000000000001/document.pdf"
	p := indexHTTPProvider(t, func(req *http.Request) (*http.Response, error) {
		switch req.Method {
		case http.MethodGet:
			listCalls++
			if req.URL.Query().Get("prefix") != "kb/" {
				t.Fatal("catalog escaped its source prefix")
			}
			if listCalls == 1 {
				return indexHTTPResponse(200, `<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>next</NextContinuationToken></ListBucketResult>`, nil), nil
			}
			if req.URL.Query().Get("continuation-token") != "next" {
				t.Fatal("catalog cursor lost")
			}
			return indexHTTPResponse(200, `<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>kb/owner/file.pdf</Key></Contents></ListBucketResult>`, nil), nil
		case http.MethodPut:
			puts++
			if req.URL.Path != "/knowledge/"+objectKey || req.Header.Get("If-None-Match") != "*" ||
				req.Header.Get("Content-Type") != "application/pdf" {
				t.Fatal("snapshot is not an immutable copy")
			}
			body, err := io.ReadAll(req.Body)
			if err != nil || string(body) != "data" {
				t.Fatal("snapshot bytes changed")
			}
		case http.MethodDelete:
			deletes++
			if req.URL.Path != "/knowledge/"+objectKey {
				t.Fatal("cleanup touched an original")
			}
		default:
			t.Fatalf("unexpected call: %s %s", req.Method, req.URL)
		}
		return indexHTTPResponse(200, "", nil), nil
	})
	keys, cursor, err := p.KnowledgePage(context.Background(), "kb/", "")
	if err != nil || len(keys) != 0 || cursor != "next" {
		t.Fatalf("empty catalog page terminated enumeration: %v %q %v", keys, cursor, err)
	}
	keys, cursor, err = p.KnowledgePage(context.Background(), "kb/", cursor)
	if err != nil || len(keys) != 1 || cursor != "" {
		t.Fatalf("catalog second page: %v %q %v", keys, cursor, err)
	}
	if err := p.Put(context.Background(), objectKey, "application/pdf", []byte("data")); err != nil {
		t.Fatal(err)
	}
	if err := p.Delete(context.Background(), objectKey); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"kb/owner/file.pdf", "shared/file.pdf", objectKey + "/../file.pdf"} {
		if err := p.Put(context.Background(), bad, "", nil); err == nil {
			t.Fatalf("write guard accepted %s", bad)
		}
		if err := p.Delete(context.Background(), bad); err == nil {
			t.Fatalf("delete guard accepted %s", bad)
		}
	}
	if _, _, err := p.KnowledgePage(context.Background(), "", ""); err == nil {
		t.Fatal("unscoped catalog accepted")
	}
	if _, err := p.HeadKnowledge(context.Background(), "docs/owner/file.pdf"); err == nil {
		t.Fatal("source reader accepted another source class")
	}
	if listCalls != 2 || puts != 1 || deletes != 1 {
		t.Fatalf("guard made unexpected calls: lists=%d puts=%d deletes=%d", listCalls, puts, deletes)
	}
}
