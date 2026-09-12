package model

import (
	"strings"
	"testing"
)

func TestKnowledgeIndexResourcePinsSourceAndVisibility(t *testing.T) {
	for _, test := range []struct {
		key, kind, prefix string
	}{
		{"kb/owner/123_notes.pdf", IndexManualKind, "manual-kb/v1/owner/"},
		{"kb/owner/설계?안%20.docx", IndexManualKind, "manual-kb/v1/owner/"},
		{"shared/reference/guide.pdf", IndexSharedKind, "shared-kb/v1/"},
	} {
		resource, ok := KnowledgeIndexResource(test.key)
		if !ok || !resource.Valid() || resource.Kind != test.kind || resource.SourceKey != test.key ||
			!strings.HasPrefix(resource.Prefix(), test.prefix) {
			t.Fatalf("source identity mismatch: %+v", resource)
		}
		if test.key == "kb/owner/123_notes.pdf" &&
			resource.ID != "229c6ceb61becda20b8c3ebbb141aa84a198a37b7c077997e7f6121180127a59" {
			t.Fatal("resource ID must hash the exact source key")
		}
		for _, mutate := range []func(*IndexResource){
			func(r *IndexResource) { r.SourceKey = "kb/other/file.pdf" },
			func(r *IndexResource) { r.Kind = "meeting" },
			func(r *IndexResource) { r.ID = strings.Repeat("0", 64) },
			func(r *IndexResource) { r.PK = "USER#other" },
		} {
			bad := resource
			mutate(&bad)
			if bad.Valid() {
				t.Fatalf("forged source accepted: %+v", bad)
			}
		}
	}
	private, _ := KnowledgeIndexResource("kb/owner/private.pdf")
	private.Kind = IndexSharedKind
	if private.Valid() {
		t.Fatal("private source promoted into shared visibility")
	}
}

func TestKnowledgeIndexResourceRejectsEscapesAndMetadata(t *testing.T) {
	for _, key := range []string{
		"kb/", "kb/owner/", "kb/../file.pdf", "kb/owner/../other.pdf",
		"kb/owner/sub/file.pdf", "kb/owner/file.pdf.metadata.json",
		"shared/", "shared//file.pdf", "shared/../private.pdf",
		"shared/file.pdf.metadata.json", "kb/owner/file\\path.pdf",
		"shared/file\n.pdf", "s3://foreign/kb/owner/file.pdf",
		"manual-kb/v1/owner/file.pdf", "shared-kb/v1/file.pdf",
		"kb/owner/" + strings.Repeat("x", 1024) + ".pdf",
	} {
		if resource, ok := KnowledgeIndexResource(key); ok {
			t.Fatalf("invalid key accepted: %q %+v", key, resource)
		}
	}
	resource, ok := CanonicalIndexResource("USER#owner", "MEETING#m")
	if !ok || !resource.Valid() {
		t.Fatal("canonical source compatibility lost")
	}
	resource.SourceKey = "kb/owner/private.pdf"
	if resource.Valid() {
		t.Fatal("canonical source cannot carry a hidden S3 authority")
	}
}
