package repository

import (
	"context"
	"strings"
	"testing"
)

func TestDocumentGrantReadsAreStronglyConsistent(t *testing.T) {
	repo := indexingWire(t, func(target string, body map[string]interface{}) string {
		if !strings.HasSuffix(target, ".GetItem") || body["ConsistentRead"] != true {
			t.Fatal("document revocation must not use an eventually consistent grant")
		}
		return "{}"
	})
	if _, err := repo.GetDocShare(context.Background(), "viewer", "doc"); err != nil {
		t.Fatal(err)
	}
}
