package service

import (
	"context"
	"errors"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func TestIndexAccountFileBindsAccountAndRecordedAuthor(t *testing.T) {
	for _, change := range []string{"valid", "sourceUserId", "accountId"} {
		t.Run(change, func(t *testing.T) {
			s, repo, objects, _, _ := newIndexTest()
			key, _ := model.CanonicalIndexResource("ACCOUNT#team", "DOC#doc")
			fields := map[string]interface{}{
				"docId": "doc", "accountId": "team", "sourceUserId": "author",
				"fileKey": "docs/author/file.pdf", "title": "Document",
			}
			if change != "valid" {
				fields[change] = "foreign"
			}
			repo.sources[key.Hash()] = &model.IndexRecord{Resource: key, Fields: fields}
			objects.asset("docs/author/file.pdf", "synthetic PDF", "version")
			_, err := s.ReadSource(context.Background(), key, false)
			if change == "valid" && err != nil {
				t.Fatal(err)
			}
			if change != "valid" && !errors.Is(err, ErrIndexInvalid) {
				t.Fatalf("mismatched %s was accepted: %v", change, err)
			}
		})
	}
}
