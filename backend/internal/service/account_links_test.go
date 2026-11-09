package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

const (
	linkAcct    = "acc1"
	linkUser    = "user1"
	linkSource  = "acct_acc1_0000abcd"
	linkHash    = "0123456789abcdef"
	linkEventID = "0f8c1d2e-3a4b-4c5d-8e6f-7a8b9c0d1e2f"
)

type linkTestRepo struct {
	member    bool
	projects  map[string]*model.Project
	meetings  map[string]*model.Meeting
	events    map[string]*model.AccountEvent
	links     map[string]*model.AccountLink
	denied    map[string]bool
	putErr    []error
	puts      []*model.AccountLink
	deletes   []string
	deleteErr []error
	listed    []model.AccountLink
}

func newLinkTestRepo() *linkTestRepo {
	return &linkTestRepo{
		member:   true,
		projects: map[string]*model.Project{"p1": {ProjectID: "p1", AccountIDs: []string{linkAcct}}, "pX": {ProjectID: "pX", AccountIDs: []string{"other"}}},
		meetings: map[string]*model.Meeting{},
		events:   map[string]*model.AccountEvent{linkEventID: {EventID: linkEventID}},
		links:    map[string]*model.AccountLink{},
		denied:   map[string]bool{},
	}
}

func (r *linkTestRepo) GetAccount(_ context.Context, id string) (*model.Account, error) {
	return &model.Account{AccountID: id}, nil
}
func (r *linkTestRepo) GetMember(context.Context, string, string) (*model.AccountMember, error) {
	if r.member {
		return &model.AccountMember{}, nil
	}
	return nil, nil
}
func (r *linkTestRepo) GetProject(_ context.Context, id string) (*model.Project, error) {
	return r.projects[id], nil
}
func (r *linkTestRepo) GetMeetingPublication(_ context.Context, owner, id string) (*model.Meeting, error) {
	return r.meetings[owner+":"+id], nil
}
func (r *linkTestRepo) GetAccountEvent(_ context.Context, _ string, id string) (*model.AccountEvent, string, error) {
	return r.events[id], "", nil
}
func (r *linkTestRepo) PutAccountLink(_ context.Context, l *model.AccountLink) error {
	r.puts = append(r.puts, l)
	if len(r.putErr) > 0 {
		err := r.putErr[0]
		r.putErr = r.putErr[1:]
		if err != nil {
			return err
		}
	}
	key := model.AccountLinkSortKey(l.Input())
	if l.Origin == model.LinkOriginAuto && r.denied[key] {
		return fmt.Errorf("%w: %w", repository.ErrConditionFailed, repository.ErrAccountLinkDenied)
	}
	if r.links[key] != nil {
		return fmt.Errorf("%w: %w", repository.ErrConditionFailed, repository.ErrAccountLinkExists)
	}
	copied := *l
	r.links[key] = &copied
	return nil
}
func (r *linkTestRepo) GetAccountLink(_ context.Context, _ string, in model.AccountLinkInput) (*model.AccountLink, error) {
	return r.links[model.AccountLinkSortKey(in)], nil
}
func (r *linkTestRepo) DeleteAccountLink(_ context.Context, _ string, in model.AccountLinkInput, deny bool) error {
	key := model.AccountLinkSortKey(in)
	r.deletes = append(r.deletes, fmt.Sprintf("%s deny=%v", key, deny))
	if len(r.deleteErr) > 0 {
		err := r.deleteErr[0]
		r.deleteErr = r.deleteErr[1:]
		if err != nil {
			return err
		}
	}
	delete(r.links, key)
	if deny {
		r.denied[key] = true
	}
	return nil
}
func (r *linkTestRepo) ListAccountLinks(context.Context, string, string, int32) ([]model.AccountLink, *string, error) {
	return r.listed, nil, nil
}

type linkTestDocs struct {
	docs map[string]*model.CrawledDocument
}

func (d *linkTestDocs) GetDocument(_ context.Context, source, hash string) (*model.CrawledDocument, error) {
	return d.docs[source+":"+hash], nil
}

type linkTestSources struct{ sources []model.CrawlerSource }

func (s *linkTestSources) accountSources(context.Context, string) ([]model.CrawlerSource, error) {
	return s.sources, nil
}

func newLinkTest() (*AccountLinksService, *linkTestRepo, *linkTestDocs, *linkTestSources) {
	repo := newLinkTestRepo()
	docs := &linkTestDocs{docs: map[string]*model.CrawledDocument{}}
	docs.docs[linkSource+":"+linkHash] = &model.CrawledDocument{Type: "news"}
	docs.docs["__tech__:techhash1"] = &model.CrawledDocument{Type: "tech"}
	docs.docs["acct_other_0000abcd:"+linkHash] = &model.CrawledDocument{Type: "news"}
	sources := &linkTestSources{sources: []model.CrawlerSource{{SourceID: linkSource, AccountID: linkAcct}}}
	svc := &AccountLinksService{repo: repo, docs: docs, sources: sources, now: func() time.Time { return time.Date(2026, 11, 9, 1, 2, 3, 0, time.UTC) }}
	return svc, repo, docs, sources
}

func publishedMeeting(owner, id string) *model.Meeting {
	return &model.Meeting{PK: model.PrefixUser + owner, SK: model.PrefixMeeting + id, UserID: owner, MeetingID: id,
		EntityType: "MEETING", AccountID: linkAcct, SharedToAccount: true}
}

var newsToProject = model.AccountLinkInput{FromKind: "news", FromID: linkSource + ":" + linkHash, ToKind: "project", ToID: "p1"}

func TestLink_CreatesManualLink(t *testing.T) {
	svc, repo, _, _ := newLinkTest()
	for _, in := range []model.AccountLinkInput{
		newsToProject,
		{FromKind: "tech", FromID: "__tech__:techhash1", ToKind: "project", ToID: "p1"},
		{FromKind: "event", FromID: linkEventID, ToKind: "project", ToID: "p1"},
	} {
		link, err := svc.Link(context.Background(), linkUser, linkAcct, in)
		if err != nil || link.Origin != "manual" || link.CreatedBy != linkUser || link.CreatedAt != "2026-11-09T01:02:03Z" || link.AccountID != linkAcct {
			t.Fatalf("%v: %#v %v", in, link, err)
		}
	}
	if len(repo.puts) != 3 {
		t.Fatalf("puts %d", len(repo.puts))
	}
}

func TestLink_RejectsProjectOutsideAccount(t *testing.T) {
	svc, repo, _, _ := newLinkTest()
	in := newsToProject
	in.ToID = "pX"
	if _, err := svc.Link(context.Background(), linkUser, linkAcct, in); !errors.Is(err, ErrInvalidInput) || len(repo.puts) != 0 {
		t.Fatalf("precheck: %v puts=%d", err, len(repo.puts))
	}
	in.ToID = "missing"
	if _, err := svc.Link(context.Background(), linkUser, linkAcct, in); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("missing project: %v", err)
	}
	// The project leaves the account between the read and the transaction.
	repo.putErr = []error{fmt.Errorf("%w: %w", repository.ErrConditionFailed, repository.ErrAccountLinkTarget)}
	if _, err := svc.Link(context.Background(), linkUser, linkAcct, newsToProject); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("transaction cancellation: %v", err)
	}
}

func TestLink_NewsFromOtherAccountSourceRejected(t *testing.T) {
	svc, repo, docs, _ := newLinkTest()
	in := newsToProject
	in.FromID = "acct_other_0000abcd:" + linkHash
	if _, err := svc.Link(context.Background(), linkUser, linkAcct, in); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("foreign source: %v", err)
	}
	docs.docs[linkSource+":"+linkHash].QualityStatus = "DELETED"
	if _, err := svc.Link(context.Background(), linkUser, linkAcct, newsToProject); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("deleted doc: %v", err)
	}
	for _, bad := range []model.AccountLinkInput{
		{FromKind: "tech", FromID: linkSource + ":" + linkHash, ToKind: "project", ToID: "p1"},
		{FromKind: "tech", FromID: "__tech__:missing", ToKind: "project", ToID: "p1"},
		{FromKind: "event", FromID: "0f8c1d2e-3a4b-4c5d-8e6f-000000000000", ToKind: "project", ToID: "p1"},
		{FromKind: "event", FromID: "not-a-uuid", ToKind: "project", ToID: "p1"},
		{FromKind: "news", FromID: linkSource, ToKind: "project", ToID: "p1"},
	} {
		if _, err := svc.Link(context.Background(), linkUser, linkAcct, bad); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%v: %v", bad, err)
		}
	}
	if len(repo.puts) != 0 {
		t.Fatalf("no write for rejected endpoints: %d", len(repo.puts))
	}
}

func TestLink_NewsToMeetingRequiresPublication(t *testing.T) {
	svc, repo, _, _ := newLinkTest()
	in := model.AccountLinkInput{FromKind: "news", FromID: newsToProject.FromID, ToKind: "meeting", ToID: "owner1:m1"}
	if _, err := svc.Link(context.Background(), linkUser, linkAcct, in); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("missing meeting: %v", err)
	}
	m := publishedMeeting("owner1", "m1")
	m.SharedToAccount = false
	repo.meetings["owner1:m1"] = m
	if _, err := svc.Link(context.Background(), linkUser, linkAcct, in); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("private meeting: %v", err)
	}
	repo.meetings["owner1:m1"] = publishedMeeting("owner1", "m1")
	if link, err := svc.Link(context.Background(), linkUser, linkAcct, in); err != nil || link.ToID != "owner1:m1" {
		t.Fatalf("published meeting: %#v %v", link, err)
	}
}

func TestLink_Idempotent(t *testing.T) {
	svc, repo, _, _ := newLinkTest()
	first, err := svc.Link(context.Background(), linkUser, linkAcct, newsToProject)
	if err != nil {
		t.Fatal(err)
	}
	svc.now = func() time.Time { return time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC) }
	second, err := svc.Link(context.Background(), "user2", linkAcct, newsToProject)
	if err != nil || second.CreatedAt != first.CreatedAt || second.CreatedBy != linkUser {
		t.Fatalf("existing row must be returned: %#v %v", second, err)
	}
	if len(repo.links) != 1 {
		t.Fatalf("links %d", len(repo.links))
	}
}

func TestUnlink_AutoWritesDeny(t *testing.T) {
	svc, repo, _, _ := newLinkTest()
	repo.meetings["owner1:m1"] = publishedMeeting("owner1", "m1")
	newsID, meetingID := newsToProject.FromID, "owner1:m1"
	if err := svc.LinkAuto(context.Background(), linkAcct, newsID, meetingID, "고객이 신규 리전 확장을 언급", "haiku-5.5"); err != nil {
		t.Fatal(err)
	}
	in := model.AccountLinkInput{FromKind: "news", FromID: newsID, ToKind: "meeting", ToID: meetingID}
	if got := repo.links[model.AccountLinkSortKey(in)]; got == nil || got.Origin != "auto" || got.Evidence == "" || got.ModelID != "haiku-5.5" || got.CreatedBy != "" {
		t.Fatalf("auto row: %#v", got)
	}
	if err := svc.Unlink(context.Background(), linkUser, linkAcct, in); err != nil {
		t.Fatal(err)
	}
	if len(repo.deletes) != 1 || repo.deletes[0] != model.AccountLinkSortKey(in)+" deny=true" {
		t.Fatalf("deletes %v", repo.deletes)
	}
	if err := svc.LinkAuto(context.Background(), linkAcct, newsID, meetingID, "quote", "haiku-5.5"); !errors.Is(err, ErrLinkSuppressed) {
		t.Fatalf("removed auto link must stay suppressed: %v", err)
	}
	// A manual link for the same pair is still allowed.
	if _, err := svc.Link(context.Background(), linkUser, linkAcct, in); err != nil {
		t.Fatalf("manual relink: %v", err)
	}
}

func TestUnlink_ManualNoDeny(t *testing.T) {
	svc, repo, _, _ := newLinkTest()
	if _, err := svc.Link(context.Background(), linkUser, linkAcct, newsToProject); err != nil {
		t.Fatal(err)
	}
	if err := svc.Unlink(context.Background(), "user2", linkAcct, newsToProject); err != nil {
		t.Fatal(err)
	}
	if len(repo.deletes) != 1 || repo.deletes[0] != model.AccountLinkSortKey(newsToProject)+" deny=false" {
		t.Fatalf("deletes %v", repo.deletes)
	}
	if err := svc.Unlink(context.Background(), linkUser, linkAcct, newsToProject); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second unlink: %v", err)
	}
	// A concurrent change makes the conditional delete fail; the reread sees it gone.
	if _, err := svc.Link(context.Background(), linkUser, linkAcct, newsToProject); err != nil {
		t.Fatal(err)
	}
	repo.deleteErr = []error{fmt.Errorf("%w: gone", repository.ErrConditionFailed)}
	delete(repo.links, model.AccountLinkSortKey(newsToProject))
	repo.links[model.AccountLinkSortKey(newsToProject)] = &model.AccountLink{Origin: "manual"}
	if err := svc.Unlink(context.Background(), linkUser, linkAcct, newsToProject); err != nil {
		t.Fatalf("retry after condition failure: %v", err)
	}
}

func TestLinkAuto_Outcomes(t *testing.T) {
	svc, repo, _, _ := newLinkTest()
	repo.meetings["owner1:m1"] = publishedMeeting("owner1", "m1")
	newsID := newsToProject.FromID
	if err := svc.LinkAuto(context.Background(), linkAcct, newsID, "owner1:m1", "q", "model"); err != nil {
		t.Fatal(err)
	}
	if err := svc.LinkAuto(context.Background(), linkAcct, newsID, "owner1:m1", "q", "model"); err != nil {
		t.Fatalf("existing link must be nil: %v", err)
	}
	for _, tc := range []struct{ meeting, evidence, model string }{
		{"owner1:m2", "q", "model"},
		{"owner1:m1", "", "model"},
		{"owner1:m1", "q", ""},
		{"m1", "q", "model"},
		{"owner1:m1", strings.Repeat("가", maxAutoLinkEvidenceRunes+1), "model"},
	} {
		if err := svc.LinkAuto(context.Background(), linkAcct, newsID, tc.meeting, tc.evidence, tc.model); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
	repo.putErr = []error{fmt.Errorf("%w: %w", repository.ErrConditionFailed, repository.ErrAccountLinkTarget)}
	repo.meetings["owner1:m3"] = publishedMeeting("owner1", "m3")
	if err := svc.LinkAuto(context.Background(), linkAcct, newsID, "owner1:m3", "q", "model"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unpublished during write: %v", err)
	}
}

func TestAccountLinks_NonMemberForbidden(t *testing.T) {
	svc, repo, _, _ := newLinkTest()
	repo.member = false
	if _, err := svc.Link(context.Background(), linkUser, linkAcct, newsToProject); !errors.Is(err, ErrForbidden) {
		t.Fatalf("link: %v", err)
	}
	if err := svc.Unlink(context.Background(), linkUser, linkAcct, newsToProject); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unlink: %v", err)
	}
	if _, _, err := svc.List(context.Background(), linkUser, linkAcct, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("list: %v", err)
	}
	if len(repo.puts) != 0 || len(repo.deletes) != 0 {
		t.Fatal("non-member must not write")
	}
}

func TestListAccountLinks_RevalidatesEndpoints(t *testing.T) {
	svc, repo, _, _ := newLinkTest()
	repo.meetings["owner1:m1"] = publishedMeeting("owner1", "m1")
	private := publishedMeeting("owner1", "m2")
	private.SharedToAccount = false
	repo.meetings["owner1:m2"] = private
	newsID := newsToProject.FromID
	repo.listed = []model.AccountLink{
		{FromKind: "news", FromID: newsID, ToKind: "project", ToID: "p1", Origin: "manual"},
		{FromKind: "news", FromID: newsID, ToKind: "project", ToID: "pX", Origin: "manual"},
		{FromKind: "news", FromID: "acct_gone_0000abcd:" + linkHash, ToKind: "project", ToID: "p1", Origin: "manual"},
		{FromKind: "news", FromID: newsID, ToKind: "meeting", ToID: "owner1:m1", Origin: "auto", Evidence: "q"},
		{FromKind: "news", FromID: newsID, ToKind: "meeting", ToID: "owner1:m2", Origin: "auto", Evidence: "q"},
		{FromKind: "tech", FromID: "__tech__:techhash1", ToKind: "project", ToID: "p1", Origin: "manual"},
		{FromKind: "event", FromID: linkEventID, ToKind: "project", ToID: "p1", Origin: "manual"},
	}
	links, _, err := svc.List(context.Background(), linkUser, linkAcct, "")
	if err != nil || len(links) != 4 {
		t.Fatalf("list: %d %v %#v", len(links), err, links)
	}
	if links[1].ToID != "owner1:m1" || links[1].Evidence != "q" {
		t.Fatalf("meeting link: %#v", links[1])
	}
}
