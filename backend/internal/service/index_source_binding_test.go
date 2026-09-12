package service

import (
	"context"
	"errors"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func TestIndexAccountFileBindsAccountAcrossEditorReplacement(t *testing.T) {
	for _, change := range []string{"valid", "later editor", "accountId"} {
		t.Run(change, func(t *testing.T) {
			s, repo, objects, _, _ := newIndexTest()
			key, _ := model.CanonicalIndexResource("ACCOUNT#team", "DOC#doc")
			fields := map[string]interface{}{
				"docId": "doc", "accountId": "team", "sourceUserId": "author",
				"fileKey": "docs/author/file.pdf", "title": "Document",
			}
			if change == "later editor" {
				fields["sourceUserId"] = "original-creator"
			} else if change != "valid" {
				fields[change] = "foreign"
			}
			repo.sources[key.Hash()] = &model.IndexRecord{Resource: key, Fields: fields}
			objects.asset("docs/author/file.pdf", "synthetic PDF", "version")
			_, err := s.ReadSource(context.Background(), key, false)
			if change != "accountId" && err != nil {
				t.Fatal(err)
			}
			if change == "accountId" && !errors.Is(err, ErrIndexInvalid) {
				t.Fatalf("mismatched %s was accepted: %v", change, err)
			}
		})
	}
}
