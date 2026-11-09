package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

const (
	accountKBPageSize     = 25
	accountKBNewsWindow   = 90 * 24 * time.Hour
	accountKBDownloadTTL  = 5 * time.Minute
	maxAccountKBTitle     = 200
	maxAccountKBTechLinks = 200
)

type accountKBRepo interface {
	GetAccount(context.Context, string) (*model.Account, error)
	GetMember(context.Context, string, string) (*model.AccountMember, error)
	GetProject(ctx context.Context, projectID string) (*model.Project, error)
	ListAccountKBRefs(ctx context.Context, accountID, cursor string, limit int32) ([]model.AccountKBRef, *string, error)
	GetAccountKBRef(ctx context.Context, accountID, refID string) (*model.AccountKBRef, error)
	DeleteAccountKBRef(ctx context.Context, accountID, refID, uploadedBy string) error
	GetIndexJob(ctx context.Context, key model.IndexResource) (*model.IndexJob, error)
	ListAccountLinksByPrefix(ctx context.Context, accountID, prefix string, maxRows int) ([]model.AccountLink, bool, error)
}

// accountKBObjects reads KB bucket objects. Head reports the object's
// account-id metadata; found is false for a missing object.
type accountKBObjects interface {
	Head(ctx context.Context, key string) (accountID string, found bool, err error)
	PresignGet(ctx context.Context, key, fileName string, ttl time.Duration) (string, error)
}

// accountKBMeetings is implemented by AccountService (publication enforced).
type accountKBMeetings interface {
	ListAccountMeetings(ctx context.Context, userID, accountID string) ([]model.MeetingRefDTO, error)
}

// accountKBNews is implemented by AccountNewsService.
type accountKBNews interface {
	CurrentNews(ctx context.Context, accountID string, from, to time.Time) ([]model.CrawledDocument, error)
}

// AccountKBService lists an account's KB material with real index status.
// Every call checks direct membership; nothing is cached across calls.
type AccountKBService struct {
	repo     accountKBRepo
	objects  accountKBObjects
	meetings accountKBMeetings
	news     accountKBNews
	docs     accountLinkDocuments
	now      func() time.Time
}

func NewAccountKBService(repo accountKBRepo, objects accountKBObjects, meetings accountKBMeetings, news accountKBNews, docs accountLinkDocuments) *AccountKBService {
	return &AccountKBService{repo: repo, objects: objects, meetings: meetings, news: news, docs: docs, now: time.Now}
}

// IndexStateLabel maps an index job to the list label. nil is UNTRACKED;
// in-progress states are PENDING. DELETED is returned as-is so callers omit it.
func IndexStateLabel(job *model.IndexJob) string {
	if job == nil {
		return model.AccountKBUntracked
	}
	switch job.State {
	case model.IndexIndexed:
		return model.AccountKBIndexed
	case model.IndexPending, model.IndexPreparing, model.IndexWaitingSync, model.IndexWaitingSource:
		return model.AccountKBPending
	case model.IndexFailed:
		return model.AccountKBFailed
	case model.IndexDeleted:
		return model.IndexDeleted
	}
	return model.AccountKBUntracked
}

func (s *AccountKBService) authorize(ctx context.Context, accountID, userID string) (*model.AccountMember, error) {
	if validateAccountHierarchyIDs(accountID, "") != nil || validateAccountHierarchyIDs(userID, "") != nil {
		return nil, ErrInvalidInput
	}
	account, err := s.repo.GetAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, ErrForbidden
	}
	member, err := s.repo.GetMember(ctx, accountID, userID)
	if err != nil {
		return nil, err
	}
	if member == nil {
		return nil, ErrForbidden
	}
	return member, nil
}

// indexState reads the job for a resource; ok=false resources are UNTRACKED.
func (s *AccountKBService) indexState(ctx context.Context, res model.IndexResource, ok bool) (string, error) {
	if !ok {
		return model.AccountKBUntracked, nil
	}
	job, err := s.repo.GetIndexJob(ctx, res)
	if errors.Is(err, repository.ErrInvalidIndexResource) {
		return model.AccountKBUntracked, nil
	}
	if err != nil {
		return "", err
	}
	return IndexStateLabel(job), nil
}

func (s *AccountKBService) knowledgeState(ctx context.Context, key string) (string, error) {
	res, ok := model.KnowledgeIndexResource(key)
	return s.indexState(ctx, res, ok)
}

// accountKBCursor is {kind}\x00{inner}: a repository cursor for uploads, an
// offset for the other kinds. The kind binds the cursor to one list.
func encodeAccountKBCursor(kind, inner string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(kind + "\x00" + inner))
}

func decodeAccountKBCursor(kind, cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(raw) > 1024 {
		return "", ErrInvalidInput
	}
	k, inner, ok := strings.Cut(string(raw), "\x00")
	if !ok || k != kind || inner == "" {
		return "", ErrInvalidInput
	}
	return inner, nil
}

func decodeOffset(kind, cursor string) (int, error) {
	inner, err := decodeAccountKBCursor(kind, cursor)
	if err != nil || inner == "" {
		return 0, err
	}
	n, err := strconv.Atoi(inner)
	if err != nil || n <= 0 || n > 100000 {
		return 0, ErrInvalidInput
	}
	return n, nil
}

func kbTitle(s string) string { return capRunes(strings.TrimSpace(s), maxAccountKBTitle) }

// List returns one page (at most 25 items) of one kind of the account's KB
// material. DELETED index states are omitted, so pages may be short.
func (s *AccountKBService) List(ctx context.Context, userID, accountID, kind, cursor string) (*model.AccountKBPage, error) {
	switch kind {
	case model.AccountKBKindUpload, model.AccountKBKindMeeting, model.AccountKBKindNews, model.AccountKBKindTech:
	default:
		return nil, ErrInvalidInput
	}
	if _, err := s.authorize(ctx, accountID, userID); err != nil {
		return nil, err
	}
	switch kind {
	case model.AccountKBKindUpload:
		return s.listUploads(ctx, accountID, cursor)
	case model.AccountKBKindMeeting:
		return s.listMeetings(ctx, userID, accountID, cursor)
	case model.AccountKBKindNews:
		return s.listNews(ctx, accountID, cursor)
	}
	return s.listTech(ctx, accountID, cursor)
}

// liveUpload reports whether the ref's object exists and still names the
// account in its signed metadata. Abandoned presigns and deleted objects
// are not live.
func (s *AccountKBService) liveUpload(ctx context.Context, accountID string, ref *model.AccountKBRef) (bool, error) {
	if ref == nil || ref.AccountID != accountID || ref.RefID != model.AccountKBRefID(ref.S3Key) {
		return false, nil
	}
	res, ok := model.KnowledgeIndexResource(ref.S3Key)
	if !ok || res.Kind != model.IndexManualKind || !strings.HasPrefix(ref.S3Key, "kb/") {
		return false, nil
	}
	tagged, found, err := s.objects.Head(ctx, ref.S3Key)
	if err != nil {
		return false, err
	}
	return found && tagged == accountID, nil
}

func (s *AccountKBService) listUploads(ctx context.Context, accountID, cursor string) (*model.AccountKBPage, error) {
	inner, err := decodeAccountKBCursor(model.AccountKBKindUpload, cursor)
	if err != nil {
		return nil, err
	}
	refs, next, err := s.repo.ListAccountKBRefs(ctx, accountID, inner, accountKBPageSize)
	if errors.Is(err, repository.ErrInvalidAccountKBCursor) {
		return nil, ErrInvalidInput
	}
	if err != nil {
		return nil, err
	}
	page := &model.AccountKBPage{Items: []model.AccountKBItem{}}
	for i := range refs {
		ref := &refs[i]
		live, err := s.liveUpload(ctx, accountID, ref)
		if err != nil {
			return nil, err
		}
		if !live {
			continue
		}
		state, err := s.knowledgeState(ctx, ref.S3Key)
		if err != nil {
			return nil, err
		}
		if state == model.IndexDeleted {
			continue
		}
		page.Items = append(page.Items, model.AccountKBItem{Kind: model.AccountKBKindUpload, ID: ref.RefID, Title: kbTitle(ref.FileName),
			Date: ref.CreatedAt.UTC().Format(time.RFC3339), UploadedBy: ref.UploadedBy, IndexState: state, CanDownload: true})
	}
	sort.SliceStable(page.Items, func(i, j int) bool { return page.Items[i].Date > page.Items[j].Date })
	if next != nil {
		page.NextCursor = encodeAccountKBCursor(model.AccountKBKindUpload, *next)
	}
	return page, nil
}

// offsetPage slices n items at offset and returns the next cursor.
func offsetPage(kind string, offset, n int) (int, int, string) {
	start := min(offset, n)
	stop := min(start+accountKBPageSize, n)
	next := ""
	if stop < n {
		next = encodeAccountKBCursor(kind, strconv.Itoa(stop))
	}
	return start, stop, next
}

func (s *AccountKBService) listMeetings(ctx context.Context, userID, accountID, cursor string) (*model.AccountKBPage, error) {
	offset, err := decodeOffset(model.AccountKBKindMeeting, cursor)
	if err != nil {
		return nil, err
	}
	refs, err := s.meetings.ListAccountMeetings(ctx, userID, accountID)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(refs, func(i, j int) bool { return refs[i].Date.After(refs[j].Date) })
	start, stop, next := offsetPage(model.AccountKBKindMeeting, offset, len(refs))
	page := &model.AccountKBPage{Items: []model.AccountKBItem{}, NextCursor: next}
	for _, ref := range refs[start:stop] {
		res, ok := model.CanonicalIndexResource("USER#"+ref.OwnerUserID, "MEETING#"+ref.MeetingID)
		state, err := s.indexState(ctx, res, ok)
		if err != nil {
			return nil, err
		}
		if state == model.IndexDeleted {
			continue
		}
		page.Items = append(page.Items, model.AccountKBItem{Kind: model.AccountKBKindMeeting, ID: ref.OwnerUserID + ":" + ref.MeetingID,
			Title: kbTitle(ref.Title), Date: ref.Date.UTC().Format(time.RFC3339), IndexState: state})
	}
	return page, nil
}

func (s *AccountKBService) docItem(ctx context.Context, kind, id string, doc *model.CrawledDocument) (model.AccountKBItem, bool, error) {
	// Index state comes from the job record only; CrawledDocument.InKB is a
	// hard-coded flag and is never shown as status.
	state, err := s.knowledgeState(ctx, doc.S3Key)
	if err != nil || state == model.IndexDeleted {
		return model.AccountKBItem{}, false, err
	}
	date := ""
	if doc.CrawledAt > 0 {
		date = time.Unix(doc.CrawledAt, 0).UTC().Format(time.RFC3339)
	}
	return model.AccountKBItem{Kind: kind, ID: id, Title: kbTitle(doc.Title), Date: date, IndexState: state}, true, nil
}

func (s *AccountKBService) listNews(ctx context.Context, accountID, cursor string) (*model.AccountKBPage, error) {
	offset, err := decodeOffset(model.AccountKBKindNews, cursor)
	if err != nil {
		return nil, err
	}
	now := s.now()
	docs, err := s.news.CurrentNews(ctx, accountID, now.Add(-accountKBNewsWindow), now)
	if err != nil {
		return nil, err
	}
	start, stop, next := offsetPage(model.AccountKBKindNews, offset, len(docs))
	page := &model.AccountKBPage{Items: []model.AccountKBItem{}, NextCursor: next}
	for i := start; i < stop; i++ {
		doc := &docs[i]
		item, ok, err := s.docItem(ctx, model.AccountKBKindNews, doc.SourceID+":"+doc.DocHash, doc)
		if err != nil {
			return nil, err
		}
		if ok {
			page.Items = append(page.Items, item)
		}
	}
	return page, nil
}

// techIDs returns the distinct tech document IDs linked to projects that
// still list the account, sorted. At most maxAccountKBTechLinks rows are read;
// truncated reports that more rows exist.
func (s *AccountKBService) techIDs(ctx context.Context, accountID string) (ids []string, truncated bool, err error) {
	links, truncated, err := s.repo.ListAccountLinksByPrefix(ctx, accountID, model.PrefixAccountLink+model.LinkKindTech+"#", maxAccountKBTechLinks)
	if err != nil {
		return nil, false, err
	}
	projects := map[string]bool{}
	seen := map[string]bool{}
	ids = []string{}
	for _, link := range links {
		if link.FromKind != model.LinkKindTech || link.ToKind != model.LinkKindProject || validateLinkShape(link.Input()) != nil || seen[link.FromID] {
			continue
		}
		ok, cached := projects[link.ToID]
		if !cached {
			project, err := s.repo.GetProject(ctx, link.ToID)
			if err != nil {
				return nil, false, err
			}
			ok = project != nil && project.ProjectID == link.ToID && contains(project.AccountIDs, accountID)
			projects[link.ToID] = ok
		}
		if ok {
			seen[link.FromID] = true
			ids = append(ids, link.FromID)
		}
	}
	sort.Strings(ids)
	return ids, truncated, nil
}

func (s *AccountKBService) listTech(ctx context.Context, accountID, cursor string) (*model.AccountKBPage, error) {
	offset, err := decodeOffset(model.AccountKBKindTech, cursor)
	if err != nil {
		return nil, err
	}
	ids, truncated, err := s.techIDs(ctx, accountID)
	if err != nil {
		return nil, err
	}
	start, stop, next := offsetPage(model.AccountKBKindTech, offset, len(ids))
	page := &model.AccountKBPage{Items: []model.AccountKBItem{}, NextCursor: next, Truncated: truncated}
	for _, id := range ids[start:stop] {
		_, hash, _ := splitLinkDocID(id)
		doc, err := s.docs.GetDocument(ctx, techLinkSourceID, hash)
		if errors.Is(err, repository.ErrInvalidDocument) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if doc == nil || doc.QualityStatus == "DELETED" || doc.Type != "tech" {
			continue
		}
		item, ok, err := s.docItem(ctx, model.AccountKBKindTech, id, doc)
		if err != nil {
			return nil, err
		}
		if ok {
			page.Items = append(page.Items, item)
		}
	}
	return page, nil
}

// uploadRef authorizes the caller and returns the account's live upload ref.
func (s *AccountKBService) uploadRef(ctx context.Context, userID, accountID, refID string) (*model.AccountMember, *model.AccountKBRef, error) {
	if !model.ValidAccountKBRefID(refID) {
		return nil, nil, ErrInvalidInput
	}
	member, err := s.authorize(ctx, accountID, userID)
	if err != nil {
		return nil, nil, err
	}
	ref, err := s.repo.GetAccountKBRef(ctx, accountID, refID)
	if err != nil {
		return nil, nil, err
	}
	if ref == nil || ref.RefID != refID {
		return nil, nil, ErrNotFound
	}
	return member, ref, nil
}

// DownloadURL returns a 5-minute presigned GET for an upload ref of this
// account whose object still exists and still names the account.
func (s *AccountKBService) DownloadURL(ctx context.Context, userID, accountID, refID string) (string, error) {
	_, ref, err := s.uploadRef(ctx, userID, accountID, refID)
	if err != nil {
		return "", err
	}
	live, err := s.liveUpload(ctx, accountID, ref)
	if err != nil {
		return "", err
	}
	if !live {
		return "", ErrNotFound
	}
	return s.objects.PresignGet(ctx, ref.S3Key, ref.FileName, accountKBDownloadTTL)
}

// Untag removes the account ref (never the S3 object). Only the uploader
// or an account owner may untag.
func (s *AccountKBService) Untag(ctx context.Context, userID, accountID, refID string) error {
	member, ref, err := s.uploadRef(ctx, userID, accountID, refID)
	if err != nil {
		return err
	}
	uploader := ""
	switch {
	case member.Role == model.RoleOwner:
	case ref.UploadedBy == userID:
		uploader = userID
	default:
		return ErrForbidden
	}
	err = s.repo.DeleteAccountKBRef(ctx, accountID, refID, uploader)
	if errors.Is(err, repository.ErrConditionFailed) {
		return ErrNotFound
	}
	return err
}

// KBObjectStore reads the KB bucket for account KB refs.
type KBObjectStore struct {
	client  *s3.Client
	presign *s3.PresignClient
	bucket  string
}

func NewKBObjectStore(client *s3.Client, bucket string) *KBObjectStore {
	return &KBObjectStore{client: client, presign: s3.NewPresignClient(client), bucket: bucket}
}

func s3Missing(err error) bool {
	var missing *s3types.NoSuchKey
	var notFound *s3types.NotFound
	var api smithy.APIError
	if errors.As(err, &missing) || errors.As(err, &notFound) {
		return true
	}
	if errors.As(err, &api) {
		switch api.ErrorCode() {
		case "NoSuchKey", "NotFound", "404":
			return true
		}
	}
	return false
}

// Head returns the object's account-id metadata; a missing object is
// found=false.
func (o *KBObjectStore) Head(ctx context.Context, key string) (string, bool, error) {
	out, err := o.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(o.bucket), Key: aws.String(key)})
	if s3Missing(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("head KB object: %w", err)
	}
	return out.Metadata["account-id"], true, nil
}

// rfc5987Escape percent-encodes every byte outside RFC 5987 attr-char.
func rfc5987Escape(v string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		c := v[i]
		if ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9') || strings.IndexByte("!#$&+-.^_`|~", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
}

// PresignGet signs a GET that downloads as an attachment.
func (o *KBObjectStore) PresignGet(ctx context.Context, key, fileName string, ttl time.Duration) (string, error) {
	in := &s3.GetObjectInput{Bucket: aws.String(o.bucket), Key: aws.String(key)}
	if fileName != "" {
		in.ResponseContentDisposition = aws.String("attachment; filename*=UTF-8''" + rfc5987Escape(fileName))
	} else {
		in.ResponseContentDisposition = aws.String("attachment")
	}
	out, err := o.presign.PresignGetObject(ctx, in, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("presign KB download: %w", err)
	}
	return out.URL, nil
}
