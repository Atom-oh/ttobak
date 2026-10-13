package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ttobak/backend/internal/model"
)

type documentReadingRepo struct {
	docs    map[string]*model.AccountDocument
	shares  map[string]*model.Share
	members map[string]*model.AccountMember
	after   func(string)
	reads   []string
}

func (r *documentReadingRepo) GetAccountDocument(_ context.Context, pk, id string) (*model.AccountDocument, error) {
	r.reads = append(r.reads, pk+"/"+id)
	doc := r.docs[pk+"/"+id]
	if doc == nil {
		return nil, nil
	}
	copy := *doc
	if r.after != nil {
		r.after(pk)
	}
	return &copy, nil
}
func (r *documentReadingRepo) GetDocShare(_ context.Context, user, id string) (*model.Share, error) {
	share := r.shares[user+"/"+id]
	if share == nil {
		return nil, nil
	}
	copy := *share
	return &copy, nil
}
func (r *documentReadingRepo) GetMember(_ context.Context, account, user string) (*model.AccountMember, error) {
	member := r.members[account+"/"+user]
	if member == nil {
		return nil, nil
	}
	copy := *member
	return &copy, nil
}
func (r *documentReadingRepo) GetAccount(context.Context, string) (*model.Account, error) {
	return &model.Account{}, nil
}

func newDocumentReader(text string) (*DocumentReadingService, *documentReadingRepo) {
	r := &documentReadingRepo{
		docs: map[string]*model.AccountDocument{"USER#owner/d1": {
			PK: "USER#owner", SK: "DOC#d1", DocID: "d1", SourceUserID: "owner",
			Title: "문서", Content: text, UpdatedAt: time.Unix(10, 0).UTC(),
		}},
		shares:  map[string]*model.Share{},
		members: map[string]*model.AccountMember{},
	}
	return NewDocumentReadingService(r), r
}

func decodeDocumentPage(t *testing.T, body []byte) map[string]interface{} {
	t.Helper()
	var page map[string]interface{}
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatal(err)
	}
	if len(body) > MaxMeetingReadingBytes {
		t.Fatalf("API exceeds its encoded bound: %d", len(body))
	}
	// Reserve the actual shape needed when structuredContent is enabled.
	envelope, err := json.Marshal(map[string]interface{}{
		"content":           []map[string]string{{"type": "text", "text": string(body)}},
		"structuredContent": json.RawMessage(body),
	})
	if err != nil || len(envelope) > 32000 {
		t.Fatalf("default page cannot fit a structured MCP envelope: %d, %v", len(envelope), err)
	}
	return page
}

func TestDocumentReadingRestoresLargeTextWithinEncodedBudget(t *testing.T) {
	for _, text := range []string{
		strings.Repeat("A", 40000),
		strings.Repeat("x", 300*1024),
		strings.Repeat("가😀\"\\\n<&>\t", 14000),
	} {
		s, _ := newDocumentReader(text)
		query := url.Values{"pageSize": {"8000"}}
		var restored strings.Builder
		offset, count := 0, 0
		for {
			body, err := s.Read(context.Background(), "owner", "", "d1", query)
			if err != nil {
				t.Fatal(err)
			}
			if len(body) > 10000 {
				t.Fatalf("default budget exceeded: %d", len(body))
			}
			value := decodeDocumentPage(t, body)
			page := value["page"].(map[string]interface{})
			if int(page["startOffset"].(float64)) != offset {
				t.Fatal("page skipped or duplicated text")
			}
			part := value["content"].(string)
			offset += utf8.RuneCountInString(part)
			if int(page["endOffset"].(float64)) != offset {
				t.Fatal("wrong Unicode offsets")
			}
			restored.WriteString(part)
			count++
			if page["complete"] == true {
				if page["nextCursor"] != nil {
					t.Fatal("complete page has continuation")
				}
				break
			}
			if part == "" || count > 200 {
				t.Fatal("page did not advance")
			}
			query.Set("cursor", page["nextCursor"].(string))
		}
		if restored.String() != text || count < 2 {
			t.Fatal("large document was not restored exactly")
		}
	}
}

func TestDocumentReadingBindsUserSelectionContentAndGrant(t *testing.T) {
	s, r := newDocumentReader(strings.Repeat("body", 100))
	r.shares["viewer/d1"] = &model.Share{OwnerID: "owner", SharedToID: "viewer", MeetingID: "d1", Permission: "read"}
	read := func(user string, q url.Values) (map[string]interface{}, error) {
		body, err := s.Read(context.Background(), user, "", "d1", q)
		if err != nil {
			return nil, err
		}
		return decodeDocumentPage(t, body), nil
	}
	first, err := read("viewer", url.Values{"pageSize": {"2"}})
	if err != nil || first["permission"] != "read" || first["scope"] != "shared" {
		t.Fatalf("shared read: %v %v", first, err)
	}
	cursor := first["page"].(map[string]interface{})["nextCursor"].(string)
	q := url.Values{"cursor": {cursor}}
	if _, err := read("owner", q); !errors.Is(err, ErrStaleReadingCursor) {
		t.Fatalf("cross-user cursor accepted: %v", err)
	}
	q.Set("section", "metadata")
	if _, err := read("viewer", q); !errors.Is(err, ErrStaleReadingCursor) {
		t.Fatalf("cross-selection cursor accepted: %v", err)
	}
	q.Del("section")
	r.docs["USER#owner/d1"].Content = "edited without timestamp change"
	if _, err := read("viewer", q); !errors.Is(err, ErrStaleReadingCursor) {
		t.Fatalf("changed body accepted: %v", err)
	}
	r.docs["USER#owner/d1"].Content = strings.Repeat("body", 100)
	r.shares["viewer/d1"].CreatedAt = time.Now()
	if _, err := read("viewer", q); !errors.Is(err, ErrStaleReadingCursor) {
		t.Fatalf("changed grant accepted: %v", err)
	}
	delete(r.shares, "viewer/d1")
	r.reads = nil
	if _, err := read("viewer", q); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked share accepted: %v", err)
	}
	if strings.Contains(strings.Join(r.reads, ","), "USER#owner") {
		t.Fatal("owner bytes read after share revocation")
	}
}

func TestDocumentReadingRechecksAccountAndConcurrentChanges(t *testing.T) {
	for _, change := range []string{"member", "document", "delete"} {
		s, r := newDocumentReader("initial")
		doc := *r.docs["USER#owner/d1"]
		doc.PK = "ACCOUNT#acct"
		r.docs["ACCOUNT#acct/d1"] = &doc
		r.members["acct/viewer"] = &model.AccountMember{AccountID: "acct", UserID: "viewer", Role: "viewer"}
		r.after = func(pk string) {
			if pk != "ACCOUNT#acct" {
				return
			}
			switch change {
			case "member":
				delete(r.members, "acct/viewer")
			case "document":
				r.docs["ACCOUNT#acct/d1"].Content = "changed"
			case "delete":
				delete(r.docs, "ACCOUNT#acct/d1")
			}
			r.after = nil
		}
		body, err := s.Read(context.Background(), "viewer", "acct", "d1", nil)
		if err == nil || body != nil {
			t.Fatalf("%s change published a stale page: %s", change, body)
		}
	}
}

type documentLinkSigner struct {
	keys                        []string
	download, preview           string
	afterDownload, afterPreview func()
	fail                        bool
}

func (s *documentLinkSigner) GeneratePresignedDownloadURLWithTTL(_ context.Context, key string, ttl time.Duration) (string, error) {
	if ttl != PublicShareURLTTL {
		return "", errors.New("unexpected URL lifetime")
	}
	s.keys = append(s.keys, key)
	if s.afterDownload != nil {
		s.afterDownload()
	}
	if s.fail {
		return "", errors.New("synthetic signing failure")
	}
	return s.download, nil
}
func (s *documentLinkSigner) GeneratePreviewPDFURLShortLived(_ context.Context, key string) (string, error) {
	s.keys = append(s.keys, key)
	if s.afterPreview != nil {
		s.afterPreview()
	}
	if s.fail {
		return "", errors.New("synthetic preview failure")
	}
	return s.preview, nil
}

func fileDocumentReader(scope string) (*DocumentReadingService, *mockAccountRepo, *documentLinkSigner, string, string, string) {
	repo := newMockAccountRepo()
	user, account, pk, keyOwner := "owner", "", "USER#owner", "owner"
	if scope == "account" {
		user, account, pk, keyOwner = "viewer", "acct", "ACCOUNT#acct", "editor"
		repo.accounts["acct"] = &model.Account{AccountID: "acct"}
		repo.members[memberKey(account, user)] = &model.AccountMember{AccountID: account, UserID: user, Role: "viewer"}
	} else if scope == "shared" {
		user = "viewer"
		repo.docShares[docShareKey(user, "d1")] = &model.Share{
			OwnerID: "owner", SharedToID: user, MeetingID: "d1", Permission: "read",
		}
	}
	repo.documents[pk] = []model.AccountDocument{{
		PK: pk, SK: "DOC#d1", DocID: "d1", Title: "slides", SourceUserID: "owner",
		FileKey: "docs/" + keyOwner + "/file.pptx", FileName: "file.pptx",
		PublicShareToken: "private-share-capability",
	}}
	signer := &documentLinkSigner{
		download: "https://media.example.invalid/download?signature=synthetic",
		preview:  "https://media.example.invalid/preview?signature=synthetic",
	}
	s := NewDocumentReadingService(repo)
	s.SetFileServices(NewAccountServiceForTest(repo), signer)
	return s, repo, signer, user, account, pk
}

func TestDocumentReadingFileLinksRetainAuthorizedPersonalAccountAndSharedDownloads(t *testing.T) {
	for _, scope := range []string{"personal", "account", "shared"} {
		s, repo, signer, user, account, pk := fileDocumentReader(scope)
		body, err := s.Read(context.Background(), user, account, "d1", nil)
		if err != nil {
			t.Fatal(err)
		}
		value := decodeDocumentPage(t, body)
		if value["mode"] != "file" || value["textStatus"] != "not_available" ||
			value["content"] != "" || value["file"].(map[string]interface{})["textExtracted"] != false ||
			value["downloadUrl"] != signer.download || value["previewUrl"] != signer.preview {
			t.Fatalf("file links or text status incorrect: %s", body)
		}
		if len(signer.keys) != 2 || signer.keys[0] != repo.documents[pk][0].FileKey ||
			signer.keys[1] != repo.documents[pk][0].FileKey {
			t.Fatal("signing did not use the canonical file key")
		}
		if strings.Contains(string(body), "private-share-capability") || value["fileKey"] != nil ||
			value["publicShareToken"] != nil {
			t.Fatal("public share capability or raw fileKey was returned")
		}
	}
}

func TestDocumentReadingRejectsWrongFileOwnershipAndTraversalBeforeSigning(t *testing.T) {
	for _, scope := range []string{"personal", "shared", "account"} {
		for _, key := range []string{"images/owner/file", "docs/owner/../other/file",
			"docs/owner/%252e%252e/other/file", "docs/owner/a%2fb", "docs/owner/back\\slash"} {
			s, repo, signer, user, account, pk := fileDocumentReader(scope)
			repo.documents[pk][0].FileKey = key
			if _, err := s.Read(context.Background(), user, account, "d1", nil); !errors.Is(err, ErrForbidden) {
				t.Fatalf("%s %s: %v", scope, key, err)
			}
			if len(signer.keys) != 0 {
				t.Fatal("invalid key was signed")
			}
		}
	}
	for _, scope := range []string{"personal", "shared"} {
		s, repo, signer, user, account, pk := fileDocumentReader(scope)
		repo.documents[pk][0].FileKey = "docs/another-user/file.pptx"
		if _, err := s.Read(context.Background(), user, account, "d1", nil); !errors.Is(err, ErrForbidden) || len(signer.keys) != 0 {
			t.Fatal("another owner's personal file was signed")
		}
	}
}

func TestDocumentReadingFileRevocationAndReplacementRejectBeforeNextURL(t *testing.T) {
	for _, scope := range []string{"personal", "shared", "account"} {
		s, repo, signer, user, account, pk := fileDocumentReader(scope)
		signer.afterDownload = func() {
			switch scope {
			case "shared":
				delete(repo.docShares, docShareKey(user, "d1"))
			case "account":
				delete(repo.members, memberKey(account, user))
			default:
				repo.documents[pk][0].FileKey = "docs/owner/replaced.pptx"
			}
		}
		body, err := s.Read(context.Background(), user, account, "d1", nil)
		if err == nil || body != nil || len(signer.keys) != 1 {
			t.Fatalf("changed file/grant continued signing: %s %v %v", scope, err, signer.keys)
		}
		signer.keys = nil
		if scope != "personal" {
			if _, err := s.Read(context.Background(), user, account, "d1", nil); err == nil || len(signer.keys) != 0 {
				t.Fatal("revoked grant authorized a later file read")
			}
		}
	}
}

func TestDocumentReadingLargeFileURLsRemainIntactAndPreviewRemainsReadable(t *testing.T) {
	s, repo, signer, user, account, pk := fileDocumentReader("shared")
	repo.documents[pk][0].Title = strings.Repeat("\n\"가", 1000)
	repo.documents[pk][0].Path = strings.Repeat("\n", 1000)
	repo.documents[pk][0].FileName = strings.Repeat("\t", 1000)
	signer.download = "https://media.example.invalid/download?" + strings.Repeat("a", 6000)
	signer.preview = "https://media.example.invalid/preview?" + strings.Repeat("b", 6000)
	body, err := s.Read(context.Background(), user, account, "d1", nil)
	if err != nil {
		t.Fatal(err)
	}
	value := decodeDocumentPage(t, body)
	if value["downloadUrl"] != signer.download || value["file"].(map[string]interface{})["previewStatus"] != "deferred" {
		t.Fatal("download truncated or preview silently dropped")
	}
	hint := value["previewRead"].(map[string]interface{})
	if hint["section"] != "preview" || hint["docId"] != "d1" || hint["accountId"] != nil || hint["expectedRevision"] != value["revision"] {
		t.Fatal("invalid personal preview selection")
	}
	query := url.Values{"section": {"preview"}, "expectedRevision": {hint["expectedRevision"].(string)}}
	body, err = s.Read(context.Background(), user, account, "d1", query)
	if err != nil {
		t.Fatal(err)
	}
	value = decodeDocumentPage(t, body)
	if value["previewUrl"] != signer.preview || value["downloadUrl"] != nil {
		t.Fatal("preview selection did not preserve the complete URL")
	}
	repo.documents[pk][0].FileKey = "docs/owner/replaced.pptx"
	if _, err := s.Read(context.Background(), user, account, "d1", query); !errors.Is(err, ErrStaleReadingCursor) {
		t.Fatal("deferred preview mixed revisions after file replacement")
	}
	delete(repo.docShares, docShareKey(user, "d1"))
	if _, err := s.Read(context.Background(), user, account, "d1", query); !errors.Is(err, ErrNotFound) {
		t.Fatal("deferred preview bypassed revoked sharing")
	}
}

func TestDocumentReadingFileLinkFailuresAreExplicit(t *testing.T) {
	s, _, signer, user, account, _ := fileDocumentReader("personal")
	signer.fail = true
	body, err := s.Read(context.Background(), user, account, "d1", nil)
	if err != nil {
		t.Fatal(err)
	}
	value := decodeDocumentPage(t, body)
	file := value["file"].(map[string]interface{})
	if value["downloadUrl"] != nil || value["previewUrl"] != nil ||
		file["downloadStatus"] != "unavailable" || file["previewStatus"] != "unavailable" {
		t.Fatal("failed best-effort signing was not surfaced")
	}
}

func TestDocumentReadingRejectsInvalidOptionsAndHandlesEmptyText(t *testing.T) {
	s, _ := newDocumentReader("")
	for _, raw := range []string{"pageSize=0", "pageSize=8001", "cursor=", "cursor=bad", "unknown=x",
		"section=notes", "pageSize=1&pageSize=2", "maxBytes=14001", "maxBytes=7999", "expectedRevision=bad"} {
		q, _ := url.ParseQuery(raw)
		if _, err := s.Read(context.Background(), "owner", "", "d1", q); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	body, err := s.Read(context.Background(), "owner", "", "d1", url.Values{"maxBytes": {"8000"}})
	if err != nil {
		t.Fatal(err)
	}
	value := decodeDocumentPage(t, body)
	if value["textStatus"] != "empty" || value["page"].(map[string]interface{})["complete"] != true {
		t.Fatalf("empty text not explicit: %s", body)
	}
	if _, err := s.Read(context.Background(), "", "", "d1", nil); !errors.Is(err, ErrForbidden) {
		t.Fatal("unauthenticated read accepted")
	}
}
