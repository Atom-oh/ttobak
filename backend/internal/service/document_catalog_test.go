package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type documentCatalogRepo struct {
	*documentReadingRepo
	queries, metadataReads int
	afterMetadata          func(string, string)
	emptyContinuation      bool
}

func (r *documentCatalogRepo) GetAccountDocument(context.Context, string, string) (*model.AccountDocument, error) {
	return nil, errors.New("catalog must not hydrate full document bodies")
}
func (r *documentCatalogRepo) GetDocumentMetadata(_ context.Context, pk, id string) (*model.AccountDocument, error) {
	r.metadataReads++
	doc := r.docs[pk+"/"+id]
	if doc == nil {
		return nil, nil
	}
	copy := *doc
	copy.Content, copy.PublicShareToken = "", ""
	if r.afterMetadata != nil {
		r.afterMetadata(pk, id)
	}
	return &copy, nil
}
func (r *documentCatalogRepo) QueryDocumentCandidates(_ context.Context, pk, prefix, after string, limit int) (*repository.DocumentCandidatePage, error) {
	r.queries++
	if r.emptyContinuation {
		r.emptyContinuation = false
		return &repository.DocumentCandidatePage{NextKey: prefix + "zzz"}, nil
	}
	keys := []string{}
	if prefix == model.PrefixDocShare {
		for _, share := range r.shares {
			if pk == model.PrefixUser+share.SharedToID {
				keys = append(keys, prefix+share.MeetingID)
			}
		}
	} else {
		for _, doc := range r.docs {
			if pk == doc.PK {
				keys = append(keys, prefix+doc.DocID)
			}
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	page := &repository.DocumentCandidatePage{}
	for _, key := range keys {
		if after != "" && key >= after {
			continue
		}
		if len(page.Rows) == limit {
			page.NextKey = page.Rows[len(page.Rows)-1].SK
			break
		}
		page.Rows = append(page.Rows, repository.DocumentCandidate{SK: key})
	}
	return page, nil
}

func newDocumentCatalogFixture() (*DocumentCatalogService, *documentCatalogRepo) {
	repo := &documentCatalogRepo{documentReadingRepo: &documentReadingRepo{
		docs: map[string]*model.AccountDocument{}, shares: map[string]*model.Share{}, members: map[string]*model.AccountMember{},
	}}
	return NewDocumentCatalogService(repo), repo
}

func decodeCatalog(t *testing.T, body []byte) map[string]interface{} {
	t.Helper()
	if len(body) > 10000 {
		t.Fatal("encoded catalog exceeded its budget")
	}
	envelope, err := json.Marshal(map[string]interface{}{
		"content":           []map[string]string{{"type": "text", "text": string(body)}},
		"structuredContent": json.RawMessage(body),
	})
	if err != nil || len(envelope) > 32000 {
		t.Fatal("catalog leaves no room for structured MCP output")
	}
	var value map[string]interface{}
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "PRIVATE_BODY") || strings.Contains(string(body), "PRIVATE_CAPABILITY") {
		t.Fatal("catalog returned document bodies or share capabilities")
	}
	return value
}

func TestDocumentCatalogRestoresLargeOwnedAndSharedCollectionsWithoutBodyReads(t *testing.T) {
	s, r := newDocumentCatalogFixture()
	largeBody := strings.Repeat("PRIVATE_BODY", 25000)
	for i := 0; i < 160; i++ {
		id := fmt.Sprintf("d-%03d", i)
		owner := "reader"
		if i >= 80 {
			owner = "author"
			r.shares["reader/"+id] = &model.Share{MeetingID: id, OwnerID: owner, SharedToID: "reader", Permission: "read"}
		}
		pk := model.PrefixUser + owner
		r.docs[pk+"/"+id] = &model.AccountDocument{PK: pk, DocID: id, Title: strings.Repeat("\n\"한😀", 500),
			Content: largeBody, PublicShareToken: "PRIVATE_CAPABILITY", DocType: "note", Links: []string{"one", "two", "three"}}
	}
	q := url.Values{"pageSize": {"50"}}
	seen := map[string]bool{}
	pages := 0
	for {
		before := r.queries
		body, err := s.Read(context.Background(), "reader", q)
		if err != nil {
			t.Fatal(err)
		}
		if r.queries != before+1 {
			t.Fatal("one catalog call queried more than one DynamoDB page")
		}
		value := decodeCatalog(t, body)
		for _, raw := range value["documents"].([]interface{}) {
			doc := raw.(map[string]interface{})
			id := doc["docId"].(string)
			if seen[id] {
				t.Fatal("duplicate document", id)
			}
			seen[id] = true
			if len(doc["metadataTruncated"].([]interface{})) == 0 {
				t.Fatal("metadata truncation was not explicit")
			}
		}
		pages++
		page := value["page"].(map[string]interface{})
		if page["complete"] == true {
			if page["nextCursor"] != nil {
				t.Fatal("complete catalog has a continuation")
			}
			break
		}
		if pages > 200 {
			t.Fatal("catalog failed to advance")
		}
		q.Set("cursor", page["nextCursor"].(string))
	}
	if len(seen) != 160 || pages < 3 {
		t.Fatalf("collection incomplete: %d documents, %d pages", len(seen), pages)
	}
}

func TestDocumentCatalogEmptyFilteredPagesAndEmptyProviderContinuationAdvance(t *testing.T) {
	s, r := newDocumentCatalogFixture()
	r.docs["USER#reader/d1"] = &model.AccountDocument{PK: "USER#reader", DocID: "d1", DocType: "other"}
	r.emptyContinuation = true
	q := url.Values{"docType": {"note"}}
	for i := 0; i < 3; i++ {
		body, err := s.Read(context.Background(), "reader", q)
		if err != nil {
			t.Fatal(err)
		}
		value := decodeCatalog(t, body)
		if len(value["documents"].([]interface{})) != 0 {
			t.Fatal("filter returned other document types")
		}
		page := value["page"].(map[string]interface{})
		if page["complete"] == true {
			if i != 2 {
				t.Fatal("collection completed before both phases")
			}
		} else {
			q.Set("cursor", page["nextCursor"].(string))
		}
	}
}

func TestDocumentCatalogCursorBindsSelectionUserAndCurrentMetadata(t *testing.T) {
	s, r := newDocumentCatalogFixture()
	r.docs["USER#reader/d1"] = &model.AccountDocument{PK: "USER#reader", DocID: "d1", Title: "old", DocType: "note"}
	body, err := s.Read(context.Background(), "reader", nil)
	if err != nil {
		t.Fatal(err)
	}
	value := decodeCatalog(t, body)
	cursor := value["page"].(map[string]interface{})["nextCursor"].(string)
	for _, q := range []url.Values{
		{"cursor": {cursor}, "docType": {"note"}},
		{"cursor": {cursor}, "docType": {"other"}},
	} {
		if _, err := s.Read(context.Background(), "reader", q); !errors.Is(err, ErrStaleReadingCursor) {
			t.Fatal("selection change accepted", err)
		}
	}
	q := url.Values{"cursor": {cursor}}
	if _, err := s.Read(context.Background(), "another", q); !errors.Is(err, ErrStaleReadingCursor) {
		t.Fatal("cross-user cursor accepted", err)
	}
	r.docs["USER#reader/d1"].Title = "edited without updating timestamp"
	if _, err := s.Read(context.Background(), "reader", q); !errors.Is(err, ErrStaleReadingCursor) {
		t.Fatal("changed anchor metadata accepted", err)
	}
}

func TestDocumentCatalogRevalidatesAccountAndSharedGrantsBeforePublication(t *testing.T) {
	for _, account := range []bool{false, true} {
		s, r := newDocumentCatalogFixture()
		pk, q := "USER#author", url.Values{}
		if account {
			pk = "ACCOUNT#acct"
			q.Set("accountId", "acct")
			r.members["acct/reader"] = &model.AccountMember{AccountID: "acct", UserID: "reader", Role: "viewer"}
		} else {
			r.shares["reader/d1"] = &model.Share{MeetingID: "d1", OwnerID: "author", SharedToID: "reader", Permission: "read"}
		}
		r.docs[pk+"/d1"] = &model.AccountDocument{PK: pk, DocID: "d1", Title: "visible"}
		if !account {
			body, err := s.Read(context.Background(), "reader", q)
			if err != nil {
				t.Fatal(err)
			}
			value := decodeCatalog(t, body)
			q.Set("cursor", value["page"].(map[string]interface{})["nextCursor"].(string))
		}
		r.afterMetadata = func(string, string) {
			if account {
				delete(r.members, "acct/reader")
			} else {
				delete(r.shares, "reader/d1")
			}
			r.afterMetadata = nil
		}
		body, err := s.Read(context.Background(), "reader", q)
		if !errors.Is(err, ErrStaleReadingCursor) || body != nil {
			t.Fatalf("revoked grant published catalog data: %s %v", body, err)
		}
	}
}

func TestDocumentCatalogRejectsInvalidOptionsAndNeverQueriesAnUnauthorizedAccount(t *testing.T) {
	s, r := newDocumentCatalogFixture()
	for _, raw := range []string{"accountId=", "accountId=../bad", "pageSize=0", "pageSize=51",
		"cursor=", "cursor=invalid", "x=y", "pageSize=1&pageSize=2"} {
		q, _ := url.ParseQuery(raw)
		if _, err := s.Read(context.Background(), "reader", q); err == nil {
			t.Fatal("accepted invalid query", raw)
		}
	}
	if _, err := s.Read(context.Background(), "reader", url.Values{"accountId": {"acct"}}); !errors.Is(err, ErrForbidden) {
		t.Fatal("unauthorized account catalog accepted", err)
	}
	if r.queries != 0 {
		t.Fatal("invalid or unauthorized request reached the catalog query")
	}
}
