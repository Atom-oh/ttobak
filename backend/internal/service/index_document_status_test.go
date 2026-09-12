package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestIndexProviderDocumentStatusesAreScopedBatchedAndOrderIndependent(t *testing.T) {
	var keys []string
	for i := 0; i < 23; i++ {
		keys = append(keys, fmt.Sprintf("canonical/v1/meeting/hash/run-%d/meeting.md", i))
	}
	calls := 0
	p := indexHTTPProvider(t, func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != http.MethodPost || req.URL.Path != "/knowledgebases/KB12345678/datasources/DS12345678/documents/getDocuments" {
			t.Fatalf("not the read-only document API: %s %s", req.Method, req.URL)
		}
		var body struct {
			Identifiers []struct {
				Type string `json:"dataSourceType"`
				S3   struct {
					URI string `json:"uri"`
				} `json:"s3"`
			} `json:"documentIdentifiers"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Identifiers) == 0 || len(body.Identifiers) > 10 {
			t.Fatal("document request exceeded the AWS 1-10 limit")
		}
		documents := []interface{}{}
		for i := len(body.Identifiers) - 1; i >= 0; i-- {
			id := body.Identifiers[i]
			if id.Type != "S3" || !strings.HasPrefix(id.S3.URI, "s3://knowledge/canonical/v1/") {
				t.Fatal("document request escaped the configured S3 source")
			}
			documents = append(documents, map[string]interface{}{
				"knowledgeBaseId": "KB12345678", "dataSourceId": "DS12345678",
				"identifier": map[string]interface{}{"dataSourceType": "S3", "s3": map[string]string{"uri": id.S3.URI}},
				"status":     "INDEXED",
			})
		}
		out, _ := json.Marshal(map[string]interface{}{"documentDetails": documents})
		return indexHTTPResponse(200, string(out), map[string]string{"Content-Type": "application/json"}), nil
	})
	statuses, err := p.Documents(context.Background(), keys)
	if err != nil || calls != 3 || len(statuses) != len(keys) {
		t.Fatalf("incomplete document status pagination: %d %d %v", calls, len(statuses), err)
	}
	for _, key := range keys {
		if statuses[key] != "INDEXED" {
			t.Fatalf("status was mapped by response order instead of URI: %s", key)
		}
	}
}

func TestIndexProviderRejectsUnboundOrIncompleteDocumentStatuses(t *testing.T) {
	for _, fault := range []string{"bucket", "kb", "source", "custom", "missing", "duplicate", "empty-status"} {
		t.Run(fault, func(t *testing.T) {
			p := indexHTTPProvider(t, func(req *http.Request) (*http.Response, error) {
				document := map[string]interface{}{
					"knowledgeBaseId": "KB12345678", "dataSourceId": "DS12345678", "status": "INDEXED",
					"identifier": map[string]interface{}{"dataSourceType": "S3",
						"s3": map[string]string{"uri": "s3://knowledge/canonical/v1/meeting/hash/run/meeting.md"}},
				}
				switch fault {
				case "bucket":
					document["identifier"] = map[string]interface{}{"dataSourceType": "S3", "s3": map[string]string{
						"uri": "s3://foreign/canonical/v1/meeting/hash/run/meeting.md"}}
				case "kb":
					document["knowledgeBaseId"] = "OTHER12345"
				case "source":
					document["dataSourceId"] = "OTHER12345"
				case "custom":
					document["identifier"] = map[string]interface{}{"dataSourceType": "CUSTOM", "custom": map[string]string{"id": "other"}}
				case "empty-status":
					document["status"] = ""
				}
				documents := []interface{}{document}
				if fault == "missing" {
					documents = nil
				} else if fault == "duplicate" {
					documents = append(documents, document)
				}
				raw, _ := json.Marshal(map[string]interface{}{"documentDetails": documents})
				return indexHTTPResponse(200, string(raw), map[string]string{"Content-Type": "application/json"}), nil
			})
			if _, err := p.Documents(context.Background(), []string{"canonical/v1/meeting/hash/run/meeting.md"}); !errors.Is(err, ErrIndexInvalid) {
				t.Fatalf("unbound/incomplete provider proof accepted: %v", err)
			}
		})
	}
}

func TestIndexProviderDocumentStatusFailuresAreNotMissingDocuments(t *testing.T) {
	for _, code := range []int{403, 429, 500} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			p := indexHTTPProvider(t, func(*http.Request) (*http.Response, error) {
				return indexHTTPResponse(code, `{"message":"synthetic outage"}`, map[string]string{
					"Content-Type": "application/json", "X-Amzn-Errortype": "InternalServerException"}), nil
			})
			if statuses, err := p.Documents(context.Background(), []string{"canonical/v1/meeting/hash/run/meeting.md"}); err == nil || len(statuses) != 0 {
				t.Fatal("provider unavailability became a document status")
			}
		})
	}
}
