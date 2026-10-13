package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type DocumentCatalogRepository interface {
	DocumentReadingRepository
	QueryDocumentCandidates(context.Context, string, string, string, int) (*repository.DocumentCandidatePage, error)
	GetDocumentMetadata(context.Context, string, string) (*model.AccountDocument, error)
}

type DocumentCatalogService struct{ repo DocumentCatalogRepository }

func NewDocumentCatalogService(repo DocumentCatalogRepository) *DocumentCatalogService {
	return &DocumentCatalogService{repo: repo}
}

type catalogMetadataView struct {
	DocumentCatalogRepository
	share *model.Share
}

func (v *catalogMetadataView) GetAccountDocument(ctx context.Context, pk, id string) (*model.AccountDocument, error) {
	return v.GetDocumentMetadata(ctx, pk, id)
}
func (v *catalogMetadataView) GetDocShare(ctx context.Context, user, id string) (*model.Share, error) {
	share, err := v.DocumentCatalogRepository.GetDocShare(ctx, user, id)
	v.share = share
	return share, err
}

type documentCatalogOptions struct {
	accountID, docType, cursor string
	pageSize                   int
}
type documentCatalogCursor struct {
	Version  int    `json:"v"`
	Binding  string `json:"binding"`
	Phase    string `json:"phase"`
	After    string `json:"after"`
	AnchorID string `json:"anchorId,omitempty"`
	Revision string `json:"revision,omitempty"`
	Grant    string `json:"grant,omitempty"`
}

func parseDocumentCatalog(q url.Values) (documentCatalogOptions, error) {
	o := documentCatalogOptions{pageSize: 20}
	for key, values := range q {
		if len(values) != 1 {
			return o, ErrInvalidReadingOptions
		}
		value := values[0]
		switch key {
		case "accountId":
			if !readingID.MatchString(value) {
				return o, ErrInvalidReadingOptions
			}
			o.accountID = value
		case "docType":
			if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 256 {
				return o, ErrInvalidReadingOptions
			}
			o.docType = value
		case "pageSize":
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 || n > 50 {
				return o, ErrInvalidReadingOptions
			}
			o.pageSize = n
		case "cursor":
			if value == "" || len(value) > 2048 {
				return o, ErrInvalidReadingCursor
			}
			o.cursor = value
		default:
			return o, ErrInvalidReadingOptions
		}
	}
	return o, nil
}

func decodeDocumentCatalogCursor(raw string) (documentCatalogCursor, error) {
	var c documentCatalogCursor
	data, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil || json.Unmarshal(data, &c) != nil || c.Version != 1 {
		return c, ErrInvalidReadingCursor
	}
	canonical, _ := json.Marshal(c)
	if !bytes.Equal(canonical, data) || (c.Phase != "own" && c.Phase != "shared") {
		return c, ErrInvalidReadingCursor
	}
	prefix := model.PrefixDoc
	if c.Phase == "shared" {
		prefix = model.PrefixDocShare
	}
	if c.After != "" && (!strings.HasPrefix(c.After, prefix) || !readingID.MatchString(strings.TrimPrefix(c.After, prefix))) {
		return c, ErrInvalidReadingCursor
	}
	if c.AnchorID != "" && (!readingID.MatchString(c.AnchorID) || len(c.Revision) != 64 || len(c.Grant) != 64) {
		return c, ErrInvalidReadingCursor
	}
	return c, nil
}

func (s *DocumentCatalogService) source(ctx context.Context, userID, accountID, docID string) (*documentReadingSource, string, error) {
	view := &catalogMetadataView{DocumentCatalogRepository: s.repo}
	source, err := NewDocumentReadingService(view).source(ctx, userID, accountID, docID)
	sharedBy := ""
	if err == nil && source.scope == "shared" && view.share != nil {
		sharedBy = view.share.OwnerEmail
	}
	return source, sharedBy, err
}

func catalogDocument(source *documentReadingSource, sharedBy string) readingObject {
	doc := source.doc
	truncated := []string{}
	dto := toDocumentDTO(doc)
	links := []string{}
	for i, link := range dto.Links {
		if i == 2 {
			truncated = append(truncated, "links")
			break
		}
		links = append(links, readingLimit(link, 128, "links", &truncated))
	}
	row := readingObject{
		"docId": doc.DocID, "scope": source.scope, "permission": source.permission,
		"title":        readingLimit(doc.Title, 128, "title", &truncated),
		"docType":      readingLimit(doc.DocType, 64, "docType", &truncated),
		"path":         readingLimit(doc.Path, 128, "path", &truncated),
		"fileName":     readingLimit(doc.FileName, 128, "fileName", &truncated),
		"sourceUserId": readingLimit(doc.SourceUserID, 128, "sourceUserId", &truncated),
		"sharedBy":     readingLimit(sharedBy, 128, "sharedBy", &truncated),
		"createdAt":    dto.CreatedAt, "updatedAt": dto.UpdatedAt, "links": links,
		"metadataRevision": documentReadingRevision(source),
	}
	row["metadataTruncated"] = truncated
	return row
}

// Read scans at most 50 candidates and one DynamoDB query per call. It never
// loads document bodies. A cursor pins selection/current account access and
// its last returned item's metadata/grant; the collection itself remains live.
func (s *DocumentCatalogService) Read(ctx context.Context, userID string, q url.Values) ([]byte, error) {
	if userID == "" {
		return nil, ErrForbidden
	}
	o, err := parseDocumentCatalog(q)
	if err != nil {
		return nil, err
	}
	accountGrant := ""
	if o.accountID != "" {
		member, err := s.repo.GetMember(ctx, o.accountID, userID)
		if err != nil {
			return nil, err
		}
		if member == nil || member.AccountID != o.accountID || member.UserID != userID {
			return nil, ErrForbidden
		}
		accountGrant = readingHash(member)
	}
	binding := readingHash([]string{"document-catalog-v1", userID, o.accountID, o.docType, accountGrant})
	c := documentCatalogCursor{Version: 1, Binding: binding, Phase: "own"}
	if o.cursor != "" {
		c, err = decodeDocumentCatalogCursor(o.cursor)
		if err != nil {
			return nil, err
		}
		if c.Binding != binding || (o.accountID != "" && c.Phase != "own") {
			return nil, ErrStaleReadingCursor
		}
		if c.AnchorID != "" {
			anchor, _, err := s.source(ctx, userID, o.accountID, c.AnchorID)
			if err != nil {
				if errors.Is(err, ErrForbidden) || errors.Is(err, ErrNotFound) {
					return nil, ErrStaleReadingCursor
				}
				return nil, err
			}
			if documentReadingRevision(anchor) != c.Revision || anchor.grant != c.Grant {
				return nil, ErrStaleReadingCursor
			}
		}
	}
	pk, prefix := model.PrefixUser+userID, model.PrefixDoc
	if o.accountID != "" {
		pk = model.PrefixAccount + o.accountID
	} else if c.Phase == "shared" {
		prefix = model.PrefixDocShare
	}
	candidates, err := s.repo.QueryDocumentCandidates(ctx, pk, prefix, c.After, o.pageSize)
	if err != nil {
		return nil, err
	}
	if candidates == nil || len(candidates.Rows) > o.pageSize {
		return nil, ErrReadingData
	}
	if candidates.NextKey != "" && (!strings.HasPrefix(candidates.NextKey, prefix) ||
		!readingID.MatchString(strings.TrimPrefix(candidates.NextKey, prefix)) || candidates.NextKey == c.After) {
		return nil, ErrReadingData
	}
	documents := []readingObject{}
	dependencies := []documentCatalogCursor{}
	more := true
	render := func(next bool) ([]byte, error) {
		var cursor interface{}
		if next {
			raw, _ := json.Marshal(c)
			cursor = base64.RawURLEncoding.EncodeToString(raw)
		}
		return readingJSON(readingObject{"documents": documents, "accountId": o.accountID,
			"consistency": "current_per_item", "page": readingObject{
				"unit": "items", "returned": len(documents), "complete": !next, "nextCursor": cursor,
			}})
	}
	consumed := 0
	for _, row := range candidates.Rows {
		id := strings.TrimPrefix(row.SK, prefix)
		if !strings.HasPrefix(row.SK, prefix) || !readingID.MatchString(id) || row.SK == c.After {
			return nil, ErrReadingData
		}
		source, sharedBy, err := s.source(ctx, userID, o.accountID, id)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		if source == nil || (c.Phase == "shared" && source.scope != "shared") ||
			(c.Phase == "own" && source.scope == "shared") || (o.docType != "" && source.doc.DocType != o.docType) {
			c.After = row.SK
			consumed++
			continue
		}
		before := c
		c.After, c.AnchorID, c.Revision, c.Grant = row.SK, id, documentReadingRevision(source), source.grant
		documents = append(documents, catalogDocument(source, sharedBy))
		body, err := render(true)
		if err != nil {
			return nil, err
		}
		if len(body) > 10000 {
			documents, c = documents[:len(documents)-1], before
			break
		}
		dependencies = append(dependencies, c)
		consumed++
	}
	if consumed == len(candidates.Rows) {
		if candidates.NextKey != "" {
			c.After = candidates.NextKey
		} else if o.accountID == "" && c.Phase == "own" {
			c.Phase, c.After = "shared", ""
		} else {
			more = false
		}
	}
	if consumed == 0 && len(candidates.Rows) > 0 {
		return nil, ErrReadingData
	}
	for _, dependency := range dependencies {
		source, _, err := s.source(ctx, userID, o.accountID, dependency.AnchorID)
		if err != nil {
			if errors.Is(err, ErrForbidden) || errors.Is(err, ErrNotFound) {
				return nil, ErrStaleReadingCursor
			}
			return nil, err
		}
		if source.grant != dependency.Grant || documentReadingRevision(source) != dependency.Revision {
			return nil, ErrStaleReadingCursor
		}
	}
	if o.accountID != "" {
		member, err := s.repo.GetMember(ctx, o.accountID, userID)
		if err != nil {
			return nil, err
		}
		if member == nil || readingHash(member) != accountGrant {
			return nil, ErrStaleReadingCursor
		}
	}
	body, err := render(more)
	if err != nil || len(body) > 10000 {
		return nil, ErrReadingData
	}
	return body, nil
}
