package service

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDocumentEvidenceExcludesStorageIdentity(t *testing.T) {
	_, r, _, result := readingFixture(t)
	attachment := *r.attachment
	attachment.ExtractedText = result
	body := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(documentEvidence(attachment)), "<DOCUMENT>\n"), "\n</DOCUMENT>")
	var wire map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &wire); err != nil {
		t.Fatal(err)
	}
	allowed := "|kind|attachmentId|name|format|scope|complete|excerpted|units|"
	for key := range wire {
		if !strings.Contains(allowed, "|"+key+"|") {
			t.Fatalf("private storage identity reached model evidence: %s", key)
		}
	}
}
