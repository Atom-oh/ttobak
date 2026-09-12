package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func TestIndexRevisionCrossLanguageVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/index-revisions.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		model.IndexResource
		Name, Outcome, Revision, ResourceHash string
		Fields                                map[string]interface{}
		Objects                               []IndexObject
	}
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, vector := range vectors {
		t.Run(vector.Name, func(t *testing.T) {
			revision, err := IndexSourceRevision(vector.IndexResource, vector.Fields, vector.Objects, vector.Outcome)
			if err != nil || revision != vector.Revision || vector.Hash() != vector.ResourceHash {
				t.Fatalf("cross-language mismatch: %s %s %v", revision, vector.Hash(), err)
			}
			if len(vector.Fields) > 0 {
				vector.Fields["content"] = "changed without updatedAt"
				changed, err := IndexSourceRevision(vector.IndexResource, vector.Fields, vector.Objects, vector.Outcome)
				if err != nil || changed == revision {
					t.Fatal("content edit did not change revision")
				}
			}
		})
	}
}

func TestIndexCanonicalKeysExcludeSharesAndState(t *testing.T) {
	for _, test := range []struct{ pk, sk, kind string }{
		{"USER#u", "MEETING#m", "meeting"}, {"USER#u", "DOC#d", "personalDocument"},
		{"ACCOUNT#a", "DOC#d", "accountDocument"}, {"USER#u", "SHARED#m", ""},
		{"USER#u", "SHAREDDOC#d", ""}, {"MEETING#m", "ATTACH#x", ""},
		{"KBINDEX#JOBS", "DOC#d", ""}, {"ACCOUNT#a", "MEETINGREF#m", ""},
		{"USER#../u", "DOC#d", ""}, {"USER#u", "DOC#../d", ""},
	} {
		r, ok := model.CanonicalIndexResource(test.pk, test.sk)
		if ok != (test.kind != "") || (ok && r.Kind != test.kind) {
			t.Fatalf("canonical classification: %+v %v", r, ok)
		}
	}
}

func TestIndexDocumentBytesRevisionAndPreviewBinding(t *testing.T) {
	for _, pk := range []string{"USER#owner", "ACCOUNT#team"} {
		t.Run(pk, func(t *testing.T) {
			s, repo, objects, p, _ := newIndexTest()
			key, _ := model.CanonicalIndexResource(pk, "DOC#d")
			repo.sources[key.Hash()] = &model.IndexRecord{Resource: key, Fields: map[string]interface{}{
				"docId": "d", "title": "문서", "content": "", "fileKey": "docs/owner/file.pdf", "updatedAt": "unchanged",
			}}
			objects.asset("docs/owner/file.pdf", "PDF bytes\x00😀", "v1")
			before, err := s.ReadSource(context.Background(), key, true)
			if err != nil {
				t.Fatal(err)
			}
			if len(before.Parts) != 1 || string(before.Parts[0].Body) != "PDF bytes\x00😀" {
				t.Fatal("file bytes replaced by filename/metadata")
			}
			if _, err := s.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			finishIndex(t, s, p)
			objects.asset("docs/owner/file.pdf", "changed PDF bytes", "v2")
			status, err := s.Status(context.Background(), key)
			if err != nil || status.State != model.IndexPending {
				t.Fatalf("same updatedAt masked changed bytes: %+v %v", status, err)
			}
			after, err := s.ReadSource(context.Background(), key, false)
			if err != nil {
				t.Fatal(err)
			}
			if before.Revision == after.Revision {
				t.Fatal("ETag/version not in revision")
			}
			repo.sources[key.Hash()].Fields["fileKey"] = "docs/owner/slides.pptx"
			objects.asset("docs/owner/slides.pptx", "pptx bytes", "source-v1")
			objects.asset("docs-pdf/owner/slides.pptx.pdf", "preview bytes", "pdf-v1")
			waiting, err := s.ReadSource(context.Background(), key, true)
			if err != nil || waiting.Outcome != model.IndexWaitingSource || len(waiting.Parts) != 0 {
				t.Fatalf("unbound PDF was treated as current: %+v %v", waiting, err)
			}
			preview := objects.heads["docs-pdf/owner/slides.pptx.pdf"]
			preview.Metadata = map[string]string{"source-etag": objects.heads["docs/owner/slides.pptx"].ETag, "source-version-id": "source-v1"}
			objects.heads[preview.Key] = preview
			ready, err := s.ReadSource(context.Background(), key, true)
			if err != nil || ready.Outcome != model.IndexIndexed || string(ready.Parts[0].Body) != "preview bytes" {
				t.Fatalf("bound preview unreadable: %+v %v", ready, err)
			}
			objects.asset("docs/owner/slides.pptx", "changed source without conversion", "source-v2")
			stale, err := s.ReadSource(context.Background(), key, true)
			if err != nil || stale.Outcome != model.IndexWaitingSource {
				t.Fatal("stale PDF was indexed")
			}
		})
	}
}

func TestIndexSourceReadPinsSpillBytesAndRejectsForeignRefs(t *testing.T) {
	s, repo, objects, _, _ := newIndexTest()
	key := addIndexMeeting(repo, "m", "정정 메모")
	record := repo.sources[key.Hash()]
	record.Fields["transcriptA"] = ""
	record.Fields["transcriptB"] = "s3://assets/transcripts/m/transcriptB.txt"
	objects.asset("transcripts/m/transcriptB.txt", "선택한 원문😀", "v1")
	before, err := s.ReadSource(context.Background(), key, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(before.Parts[0].Body), "선택한 원문😀") {
		t.Fatal("spill pointer leaked instead of text")
	}
	objects.asset("transcripts/m/transcriptB.txt", "수정한 원문😀", "v2")
	after, err := s.ReadSource(context.Background(), key, false)
	if err != nil || before.Revision == after.Revision {
		t.Fatal("spill bytes not bound to revision")
	}
	objects.beforeRead = func() { objects.asset("transcripts/m/transcriptB.txt", "racing bytes", "v3") }
	if _, err := s.ReadSource(context.Background(), key, true); !errors.Is(err, ErrIndexChanged) {
		t.Fatalf("changed object read accepted: %v", err)
	}
	record.Fields["transcriptB"] = "s3://assets/transcripts/foreign/transcriptB.txt"
	if _, err := s.ReadSource(context.Background(), key, true); err == nil {
		t.Fatal("foreign meeting spill accepted")
	}
}

func TestIndexMetadataCarriesCanonicalIdentityAndByteBindings(t *testing.T) {
	s, repo, _, _, _ := newIndexTest()
	key := addIndexMeeting(repo, "m", "notes")
	snapshot, err := s.ReadSource(context.Background(), key, true)
	if err != nil {
		t.Fatal(err)
	}
	data, err := indexMetadata(key, snapshot, "run")
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		MetadataAttributes map[string]interface{} `json:"metadataAttributes"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"sourcePK": "USER#owner", "sourceSK": "MEETING#m", "resourceKind": "meeting", "sourceRevision": snapshot.Revision, "indexSchema": "canonical-v1"} {
		if envelope.MetadataAttributes[name] != want {
			t.Fatalf("metadata %s = %v", name, envelope.MetadataAttributes[name])
		}
	}
	if _, ok := envelope.MetadataAttributes["sourceObjects"].(string); !ok {
		t.Fatal("object bindings are not a JSON string")
	}
}

func TestIndexSourceRejectsInvalidTextAndOversizedMetadata(t *testing.T) {
	s, repo, objects, _, _ := newIndexTest()
	key, _ := model.CanonicalIndexResource("USER#owner", "DOC#d")
	repo.sources[key.Hash()] = &model.IndexRecord{Resource: key, Fields: map[string]interface{}{
		"docId": "d", "fileKey": "docs/owner/note.txt",
	}}
	objects.asset("docs/owner/note.txt", "\xff\xfeinvalid UTF-8", "v1")
	if _, err := s.ReadSource(context.Background(), key, true); !errors.Is(err, ErrIndexInvalid) {
		t.Fatalf("unsupported text encoding was published: %v", err)
	}
	objects.asset("docs/owner/note.txt", "", "v2")
	empty, err := s.ReadSource(context.Background(), key, true)
	if err != nil || empty.Outcome != model.IndexWaitingSource || len(empty.Parts) != 0 {
		t.Fatalf("empty source claimed indexable: %+v %v", empty, err)
	}
	objects.asset("docs/owner/note.txt", "valid text", strings.Repeat("v", 11000))
	if _, err := s.ReadSource(context.Background(), key, false); !errors.Is(err, ErrIndexInvalid) {
		t.Fatalf("oversized metadata was publishable: %v", err)
	}
}
