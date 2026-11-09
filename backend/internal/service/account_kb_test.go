package service

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/connector"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type kbFakeRepo struct {
	members  map[string]string // userID -> role
	refs     map[string]*model.AccountKBRef
	jobs     map[string]*model.IndexJob // resource hash/ID -> job
	projects map[string]*model.Project
	links    []model.AccountLink
	linksCut bool     // ListAccountLinksByPrefix reports truncation
	deletes  []string // refID|uploadedBy
}

func (f *kbFakeRepo) GetAccount(_ context.Context, id string) (*model.Account, error) {
	if id != "acc" {
		return nil, nil
	}
	return &model.Account{AccountID: id}, nil
}
func (f *kbFakeRepo) GetMember(_ context.Context, acc, user string) (*model.AccountMember, error) {
	role, ok := f.members[user]
	if !ok || acc != "acc" {
		return nil, nil
	}
	return &model.AccountMember{AccountID: acc, UserID: user, Role: role}, nil
}
func (f *kbFakeRepo) GetProject(_ context.Context, id string) (*model.Project, error) {
	return f.projects[id], nil
}
func (f *kbFakeRepo) ListAccountKBRefs(_ context.Context, acc, cursor string, limit int32) ([]model.AccountKBRef, *string, error) {
	out := []model.AccountKBRef{}
	for _, r := range f.refs {
		if r.AccountID == acc {
			out = append(out, *r)
		}
	}
	return out, nil, nil
}
func (f *kbFakeRepo) GetAccountKBRef(_ context.Context, acc, id string) (*model.AccountKBRef, error) {
	r := f.refs[id]
	if r == nil || r.AccountID != acc {
		return nil, nil
	}
	return r, nil
}
func (f *kbFakeRepo) DeleteAccountKBRef(_ context.Context, acc, id, uploadedBy string) error {
	r := f.refs[id]
	if r == nil || (uploadedBy != "" && r.UploadedBy != uploadedBy) {
		return repository.ErrConditionFailed
	}
	f.deletes = append(f.deletes, id+"|"+uploadedBy)
	delete(f.refs, id)
	return nil
}
func (f *kbFakeRepo) GetIndexJob(_ context.Context, key model.IndexResource) (*model.IndexJob, error) {
	return f.jobs[key.SK], nil
}
func (f *kbFakeRepo) ListAccountLinksByPrefix(_ context.Context, acc, prefix string, maxRows int) ([]model.AccountLink, bool, error) {
	out := []model.AccountLink{}
	for _, l := range f.links {
		if strings.HasPrefix(model.AccountLinkSortKey(l.Input()), prefix) {
			out = append(out, l)
		}
	}
	return out, f.linksCut, nil
}

type kbFakeObjects struct {
	tags     map[string]string // key -> account-id metadata; absent = missing object
	presigns []string
}

func (o *kbFakeObjects) Head(_ context.Context, key string) (string, bool, error) {
	tag, ok := o.tags[key]
	return tag, ok, nil
}
func (o *kbFakeObjects) PresignGet(_ context.Context, key, name string, ttl time.Duration) (string, error) {
	if ttl != 5*time.Minute {
		return "", errors.New("wrong ttl")
	}
	o.presigns = append(o.presigns, key)
	return "https://signed.example/" + url.PathEscape(name), nil
}

type kbFakeMeetings struct{ refs []model.MeetingRefDTO }

func (m kbFakeMeetings) ListAccountMeetings(context.Context, string, string) ([]model.MeetingRefDTO, error) {
	return append([]model.MeetingRefDTO(nil), m.refs...), nil
}

type kbFakeNews struct{ docs []model.CrawledDocument }

func (n kbFakeNews) CurrentNews(_ context.Context, _ string, from, to time.Time) ([]model.CrawledDocument, error) {
	if to.Sub(from) != 90*24*time.Hour {
		return nil, errors.New("wrong window")
	}
	return n.docs, nil
}

type kbFakeDocs map[string]*model.CrawledDocument

func (d kbFakeDocs) GetDocument(_ context.Context, source, hash string) (*model.CrawledDocument, error) {
	return d[source+":"+hash], nil
}

func kbRef(user, name string) *model.AccountKBRef {
	key := "kb/" + user + "/1700000000000_" + name
	return &model.AccountKBRef{AccountID: "acc", RefID: model.AccountKBRefID(key), S3Key: key, FileName: name, UploadedBy: user,
		CreatedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}
}

func knowledgeSK(key string) string {
	r, _ := model.KnowledgeIndexResource(key)
	return r.SK
}

func newKBFixture() (*AccountKBService, *kbFakeRepo, *kbFakeObjects) {
	repo := &kbFakeRepo{members: map[string]string{"owner": model.RoleOwner, "u1": model.RoleSA, "u2": model.RoleAM},
		refs: map[string]*model.AccountKBRef{}, jobs: map[string]*model.IndexJob{}, projects: map[string]*model.Project{}}
	objects := &kbFakeObjects{tags: map[string]string{}}
	svc := NewAccountKBService(repo, objects, kbFakeMeetings{}, kbFakeNews{}, kbFakeDocs{})
	return svc, repo, objects
}

func addUpload(repo *kbFakeRepo, objects *kbFakeObjects, user, name string, uploaded bool) *model.AccountKBRef {
	ref := kbRef(user, name)
	repo.refs[ref.RefID] = ref
	if uploaded {
		objects.tags[ref.S3Key] = "acc"
	}
	return ref
}

func TestIndexStateLabel(t *testing.T) {
	cases := []struct {
		state, want string
	}{
		{model.IndexPending, "PENDING"},
		{model.IndexPreparing, "PENDING"},
		{model.IndexWaitingSync, "PENDING"},
		{model.IndexWaitingSource, "PENDING"},
		{model.IndexIndexed, "INDEXED"},
		{model.IndexDeleted, "DELETED"},
		{model.IndexFailed, "FAILED"},
		{"SOMETHING_NEW", "UNTRACKED"},
	}
	for _, c := range cases {
		if got := IndexStateLabel(&model.IndexJob{State: c.state}); got != c.want {
			t.Errorf("%s: got %s want %s", c.state, got, c.want)
		}
	}
	if got := IndexStateLabel(nil); got != "UNTRACKED" {
		t.Errorf("nil: got %s", got)
	}
}

func TestPresign_AccountUploadWritesRef(t *testing.T) {
	svc := newKBUploadService(map[string]bool{"acct-a/owner": true})
	var refs []*model.AccountKBRef
	svc.SetAccountKBRefWriter(func(_ context.Context, ref *model.AccountKBRef) error {
		refs = append(refs, ref)
		return nil
	})
	got, err := svc.GetPresignedURL(context.Background(), "owner", "my file.pdf", "application/pdf", "acct-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Fatalf("refs written: %d", len(refs))
	}
	ref := refs[0]
	if ref.AccountID != "acct-a" || ref.S3Key != got.Key || ref.RefID != model.AccountKBRefID(got.Key) ||
		ref.UploadedBy != "owner" || ref.FileName != "my_file.pdf" || ref.CreatedAt.IsZero() {
		t.Fatalf("ref: %+v", ref)
	}
	// Personal uploads write no ref.
	if _, err := svc.GetPresignedURL(context.Background(), "owner", "a.pdf", "application/pdf", ""); err != nil || len(refs) != 1 {
		t.Fatalf("personal upload wrote a ref: %d %v", len(refs), err)
	}
	// A failed ref write fails the presign.
	svc.SetAccountKBRefWriter(func(context.Context, *model.AccountKBRef) error { return repository.ErrConditionFailed })
	if out, err := svc.GetPresignedURL(context.Background(), "owner", "a.pdf", "application/pdf", "acct-a"); err == nil || out != nil {
		t.Fatalf("presign must fail when the ref write fails: %v", err)
	}
	// No writer configured refuses account uploads.
	svc.SetAccountKBRefWriter(nil)
	if _, err := svc.GetPresignedURL(context.Background(), "owner", "a.pdf", "application/pdf", "acct-a"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("no writer: %v", err)
	}
}

func TestAccountKB_ListOmitsMissingObject(t *testing.T) {
	svc, repo, objects := newKBFixture()
	live := addUpload(repo, objects, "u1", "live.pdf", true)
	addUpload(repo, objects, "u1", "abandoned.pdf", false)
	retagged := addUpload(repo, objects, "u1", "other.pdf", true)
	objects.tags[retagged.S3Key] = "different-account"
	repo.jobs[knowledgeSK(live.S3Key)] = &model.IndexJob{State: model.IndexWaitingSync}

	page, err := svc.List(context.Background(), "u2", "acc", "upload", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("items: %+v", page.Items)
	}
	it := page.Items[0]
	if it.ID != live.RefID || it.Title != "live.pdf" || it.UploadedBy != "u1" || it.IndexState != "PENDING" || !it.CanDownload || it.Kind != "upload" {
		t.Fatalf("item: %+v", it)
	}
	// A DELETED index state is omitted.
	repo.jobs[knowledgeSK(live.S3Key)] = &model.IndexJob{State: model.IndexDeleted}
	page, err = svc.List(context.Background(), "u2", "acc", "upload", "")
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("deleted item listed: %+v %v", page, err)
	}
}

func TestAccountKB_RemovedMemberCannotDownload(t *testing.T) {
	svc, repo, objects := newKBFixture()
	ref := addUpload(repo, objects, "u1", "a.pdf", true)
	u, err := svc.DownloadURL(context.Background(), "u2", "acc", ref.RefID)
	if err != nil || !strings.HasPrefix(u, "https://signed.example/") || len(objects.presigns) != 1 || objects.presigns[0] != ref.S3Key {
		t.Fatalf("member download: %q %v %v", u, err, objects.presigns)
	}
	delete(repo.members, "u2")
	if _, err := svc.DownloadURL(context.Background(), "u2", "acc", ref.RefID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("removed member: %v", err)
	}
	if _, err := svc.List(context.Background(), "u2", "acc", "upload", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("removed member list: %v", err)
	}
	if len(objects.presigns) != 1 {
		t.Fatal("presigned for a removed member")
	}
}

func TestAccountKB_DownloadRejectsBadRefs(t *testing.T) {
	svc, repo, objects := newKBFixture()
	for _, id := range []string{"", "ABC", strings.Repeat("A", 64), "../" + strings.Repeat("a", 61), strings.Repeat("a", 65)} {
		if _, err := svc.DownloadURL(context.Background(), "u1", "acc", id); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%q: %v", id, err)
		}
	}
	// A ref whose key is outside kb/ or traverses is never presigned.
	for _, key := range []string{"shared/news/x/y.md", "kb/u1/../u2/a.pdf", "audio/u1/m/a.webm"} {
		bad := &model.AccountKBRef{AccountID: "acc", RefID: model.AccountKBRefID(key), S3Key: key, FileName: "x", UploadedBy: "u1"}
		repo.refs[bad.RefID] = bad
		objects.tags[key] = "acc"
		if _, err := svc.DownloadURL(context.Background(), "u1", "acc", bad.RefID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%q: %v", key, err)
		}
	}
	// A ref ID that does not match its key is rejected.
	ref := kbRef("u1", "a.pdf")
	ref.RefID = strings.Repeat("b", 64)
	repo.refs[ref.RefID] = ref
	objects.tags[ref.S3Key] = "acc"
	if _, err := svc.DownloadURL(context.Background(), "u1", "acc", ref.RefID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("mismatched ref: %v", err)
	}
	// Missing object.
	gone := addUpload(repo, objects, "u1", "gone.pdf", false)
	if _, err := svc.DownloadURL(context.Background(), "u1", "acc", gone.RefID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing object: %v", err)
	}
	if len(objects.presigns) != 0 {
		t.Fatalf("presigned: %v", objects.presigns)
	}
}

func TestAccountKB_UntagByUploaderOrOwnerOnly(t *testing.T) {
	svc, repo, objects := newKBFixture()
	ref := addUpload(repo, objects, "u1", "a.pdf", true)
	if err := svc.Untag(context.Background(), "u2", "acc", ref.RefID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("other member: %v", err)
	}
	if err := svc.Untag(context.Background(), "u1", "acc", ref.RefID); err != nil {
		t.Fatalf("uploader: %v", err)
	}
	if len(repo.deletes) != 1 || repo.deletes[0] != ref.RefID+"|u1" {
		t.Fatalf("uploader delete must be conditioned on the uploader: %v", repo.deletes)
	}
	if _, ok := objects.tags[ref.S3Key]; !ok {
		t.Fatal("untag must never delete the object")
	}
	ref2 := addUpload(repo, objects, "u2", "b.pdf", true)
	if err := svc.Untag(context.Background(), "owner", "acc", ref2.RefID); err != nil {
		t.Fatalf("owner: %v", err)
	}
	if err := svc.Untag(context.Background(), "owner", "acc", ref2.RefID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second untag: %v", err)
	}
	ref3 := addUpload(repo, objects, "u1", "c.pdf", true)
	delete(repo.members, "u1")
	if err := svc.Untag(context.Background(), "u1", "acc", ref3.RefID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("departed uploader: %v", err)
	}
}

func TestAccountKB_UploaderLeftStillListed(t *testing.T) {
	svc, repo, objects := newKBFixture()
	ref := addUpload(repo, objects, "u1", "a.pdf", true)
	delete(repo.members, "u1")
	page, err := svc.List(context.Background(), "u2", "acc", "upload", "")
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != ref.RefID || page.Items[0].UploadedBy != "u1" {
		t.Fatalf("departed uploader's file: %+v %v", page, err)
	}
	if _, err := svc.DownloadURL(context.Background(), "u2", "acc", ref.RefID); err != nil {
		t.Fatalf("download: %v", err)
	}
}

func TestAccountKB_NeverUsesInKBFlag(t *testing.T) {
	svc, repo, _ := newKBFixture()
	news := model.CrawledDocument{DocHash: "h1", SourceID: "src", Type: "news", Title: "N", S3Key: "shared/news/src/h1.md", CrawledAt: 1700000000, InKB: true}
	versioned := model.CrawledDocument{DocHash: "h2", SourceID: "src", Type: "news", Title: "V", S3Key: "news/v1/src/h2/t.md", CrawledAt: 1700000000, InKB: true}
	svc.news = kbFakeNews{docs: []model.CrawledDocument{news, versioned}}
	page, err := svc.List(context.Background(), "u1", "acc", "news", "")
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("news: %+v %v", page, err)
	}
	for _, it := range page.Items {
		if it.IndexState != "UNTRACKED" || it.CanDownload {
			t.Fatalf("inKB leaked into status: %+v", it)
		}
	}
	if page.Items[0].ID != "src:h1" {
		t.Fatalf("id: %+v", page.Items[0])
	}
	repo.jobs[knowledgeSK(news.S3Key)] = &model.IndexJob{State: model.IndexIndexed}
	page, _ = svc.List(context.Background(), "u1", "acc", "news", "")
	if page.Items[0].IndexState != "INDEXED" {
		t.Fatalf("job state ignored: %+v", page.Items[0])
	}
}

func TestAccountKB_MeetingsAndTech(t *testing.T) {
	svc, repo, _ := newKBFixture()
	svc.meetings = kbFakeMeetings{refs: []model.MeetingRefDTO{
		{MeetingID: "m1", OwnerUserID: "u1", Title: "Old", Date: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{MeetingID: "m2", OwnerUserID: "u2", Title: "New", Date: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)},
	}}
	res, _ := model.CanonicalIndexResource("USER#u1", "MEETING#m1")
	repo.jobs[res.SK] = &model.IndexJob{State: model.IndexFailed}
	page, err := svc.List(context.Background(), "u1", "acc", "meeting", "")
	if err != nil || len(page.Items) != 2 || page.Items[0].ID != "u2:m2" || page.Items[0].IndexState != "UNTRACKED" ||
		page.Items[1].ID != "u1:m1" || page.Items[1].IndexState != "FAILED" {
		t.Fatalf("meetings: %+v %v", page, err)
	}

	repo.projects["p1"] = &model.Project{ProjectID: "p1", AccountIDs: []string{"acc"}}
	repo.projects["p2"] = &model.Project{ProjectID: "p2", AccountIDs: []string{"other"}}
	repo.links = []model.AccountLink{
		{FromKind: "tech", FromID: "__tech__:t1", ToKind: "project", ToID: "p1"},
		{FromKind: "tech", FromID: "__tech__:t1", ToKind: "project", ToID: "p1x"},
		{FromKind: "tech", FromID: "__tech__:t2", ToKind: "project", ToID: "p2"},
		{FromKind: "tech", FromID: "__tech__:t3", ToKind: "project", ToID: "p1"},
	}
	svc.docs = kbFakeDocs{
		"__tech__:t1": {DocHash: "t1", Type: "tech", Title: "Lambda", S3Key: "shared/aws-docs/lambda/t1.md"},
		"__tech__:t2": {DocHash: "t2", Type: "tech", Title: "Other account"},
		"__tech__:t3": {DocHash: "t3", Type: "tech", Title: "Deleted", QualityStatus: "DELETED"},
	}
	page, err = svc.List(context.Background(), "u1", "acc", "tech", "")
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != "__tech__:t1" || page.Items[0].Title != "Lambda" || page.Truncated {
		t.Fatalf("tech: %+v %v", page, err)
	}
	repo.linksCut = true
	if page, err = svc.List(context.Background(), "u1", "acc", "tech", ""); err != nil || !page.Truncated {
		t.Fatalf("tech link bound not surfaced: %+v %v", page, err)
	}
}

func TestAccountKB_PagingAndInputs(t *testing.T) {
	svc, _, _ := newKBFixture()
	refs := []model.MeetingRefDTO{}
	for i := 0; i < 30; i++ {
		refs = append(refs, model.MeetingRefDTO{MeetingID: "m" + string(rune('a'+i)), OwnerUserID: "u1", Date: time.Unix(int64(i), 0)})
	}
	svc.meetings = kbFakeMeetings{refs: refs}
	page, err := svc.List(context.Background(), "u1", "acc", "meeting", "")
	if err != nil || len(page.Items) != 25 || page.NextCursor == "" {
		t.Fatalf("page 1: %d %q %v", len(page.Items), page.NextCursor, err)
	}
	page2, err := svc.List(context.Background(), "u1", "acc", "meeting", page.NextCursor)
	if err != nil || len(page2.Items) != 5 || page2.NextCursor != "" {
		t.Fatalf("page 2: %+v %v", page2, err)
	}
	if _, err := svc.List(context.Background(), "u1", "acc", "news", page.NextCursor); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cursor kind not bound: %v", err)
	}
	for _, kind := range []string{"", "all", "UPLOAD"} {
		if _, err := svc.List(context.Background(), "u1", "acc", kind, ""); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("kind %q: %v", kind, err)
		}
	}
	if _, err := svc.List(context.Background(), "stranger", "acc", "meeting", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-member: %v", err)
	}
}

func TestRFC5987Escape(t *testing.T) {
	if got := rfc5987Escape("a b'(x);\"한.pdf"); got != "a%20b%27%28x%29%3B%22%ED%95%9C.pdf" {
		t.Fatalf("got %s", got)
	}
}

func connectorCapsFn(caps ...string) func(context.Context) ([]string, bool) {
	return func(context.Context) ([]string, bool) { return caps, true }
}

func TestPresign_ConnectorAccountUploadRequiresShare(t *testing.T) {
	svc := newKBUploadService(map[string]bool{"acct-a/owner": true})
	writes := 0
	svc.SetAccountKBRefWriter(func(context.Context, *model.AccountKBRef) error { writes++; return nil })
	svc.connectorCaps = connectorCapsFn("read", "write")
	out, err := svc.GetPresignedURL(context.Background(), "owner", "a.pdf", "application/pdf", "acct-a")
	if !errors.Is(err, connector.ErrCapabilityDenied) || out != nil || writes != 0 {
		t.Fatalf("connector without share: %v %v writes=%d", out, err, writes)
	}
	svc.connectorCaps = connectorCapsFn("read", "share", "write")
	if _, err := svc.GetPresignedURL(context.Background(), "owner", "a.pdf", "application/pdf", "acct-a"); err != nil || writes != 1 {
		t.Fatalf("connector with share: %v writes=%d", err, writes)
	}
	// Share does not bypass membership.
	if _, err := svc.GetPresignedURL(context.Background(), "owner", "a.pdf", "application/pdf", "acct-b"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-member with share: %v", err)
	}
	// A direct user request (no verified principal) is unchanged.
	svc.connectorCaps = verifiedConnectorCapabilities
	if _, err := svc.GetPresignedURL(context.Background(), "owner", "a.pdf", "application/pdf", "acct-a"); err != nil || writes != 2 {
		t.Fatalf("direct request: %v writes=%d", err, writes)
	}
}

func TestPresign_ConnectorPersonalUploadUnchanged(t *testing.T) {
	svc := newKBUploadService(map[string]bool{})
	writes := 0
	svc.SetAccountKBRefWriter(func(context.Context, *model.AccountKBRef) error { writes++; return nil })
	svc.connectorCaps = connectorCapsFn("read", "write")
	got, err := svc.GetPresignedURL(context.Background(), "owner", "a.pdf", "application/pdf", "")
	if err != nil || got == nil || got.Headers != nil || writes != 0 {
		t.Fatalf("personal connector upload: %+v %v writes=%d", got, err, writes)
	}
}
