package service

import (
	"context"
	"encoding/hex"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ttobak/backend/internal/model"
)

// The production repository performs strongly consistent primary-key reads for
// all four methods. A cursor never substitutes for a present authorization read.
type DocumentReadingRepository interface {
	GetAccountDocument(context.Context, string, string) (*model.AccountDocument, error)
	GetDocShare(context.Context, string, string) (*model.Share, error)
	GetMember(context.Context, string, string) (*model.AccountMember, error)
	GetAccount(context.Context, string) (*model.Account, error)
}

type DocumentReadingFiles interface {
	GetUserDocument(context.Context, string, string) (*model.AccountDocumentDetail, error)
	GetAccountDocument(context.Context, string, string, string) (*model.AccountDocumentDetail, error)
}

type DocumentReadingSigner interface {
	GeneratePresignedDownloadURLWithTTL(context.Context, string, time.Duration) (string, error)
	GeneratePreviewPDFURLShortLived(context.Context, string) (string, error)
}

type DocumentReadingService struct {
	repo   DocumentReadingRepository
	files  DocumentReadingFiles
	signer DocumentReadingSigner
}

func NewDocumentReadingService(repo DocumentReadingRepository) *DocumentReadingService {
	return &DocumentReadingService{repo: repo}
}

func (s *DocumentReadingService) SetFileServices(files DocumentReadingFiles, signer DocumentReadingSigner) {
	s.files, s.signer = files, signer
}

func parseDocumentReading(q url.Values) (ReadingOptions, error) {
	// 10KB leaves room for both JSON text and structuredContent in a 32KB MCP
	// result. The API's absolute 14KB bound is retained.
	o := ReadingOptions{Section: "content", PageSize: 4000, MaxBytes: 10000}
	for key, values := range q {
		if len(values) != 1 {
			return o, ErrInvalidReadingOptions
		}
		value := values[0]
		switch key {
		case "section":
			if value != "content" && value != "metadata" && value != "download" && value != "preview" {
				return o, ErrInvalidReadingOptions
			}
			o.Section = value
		case "pageSize", "maxBytes":
			n, err := strconv.Atoi(value)
			if err != nil {
				return o, ErrInvalidReadingOptions
			}
			if key == "pageSize" {
				if n < 1 || n > 8000 {
					return o, ErrInvalidReadingOptions
				}
				o.PageSize = n
			} else {
				if n < 8000 || n > MaxMeetingReadingBytes {
					return o, ErrInvalidReadingOptions
				}
				o.MaxBytes = n
			}
		case "cursor":
			if len(value) > 2048 {
				return o, ErrInvalidReadingCursor
			}
			if _, err := decodeReadingCursor(value); err != nil {
				return o, err
			}
			o.Cursor = value
		case "expectedRevision":
			if _, err := hex.DecodeString(value); err != nil || len(value) != 64 || value != strings.ToLower(value) {
				return o, ErrInvalidReadingOptions
			}
			o.ExpectedRevision = value
		default:
			return o, ErrInvalidReadingOptions
		}
	}
	return o, nil
}

type documentReadingSource struct {
	doc               *model.AccountDocument
	scope, permission string
	grant             string
}

func (s *DocumentReadingService) source(ctx context.Context, userID, accountID, docID string) (*documentReadingSource, error) {
	pk, scope, permission, grant := model.PrefixUser+userID, "personal", "owner", readingHash(userID)
	if accountID != "" {
		accessGrant, err := resolveAccountReadGrant(ctx, s.repo, accountID, userID)
		if err != nil {
			return nil, err
		}
		if accessGrant == nil {
			account, err := s.repo.GetAccount(ctx, accountID)
			if err != nil {
				return nil, err
			}
			if account == nil {
				return nil, ErrNotFound
			}
			return nil, ErrForbidden
		}
		if !accessGrant.inherited() && (accessGrant.Member.AccountID != accountID || accessGrant.Member.UserID != userID) {
			return nil, ErrForbidden
		}
		pk, scope, permission, grant = model.PrefixAccount+accountID, "account", accessGrant.permission(), accessGrant.fingerprint()
	}
	doc, err := s.repo.GetAccountDocument(ctx, pk, docID)
	if err != nil {
		return nil, err
	}
	if doc == nil && accountID == "" {
		share, err := s.repo.GetDocShare(ctx, userID, docID)
		if err != nil {
			return nil, err
		}
		if share == nil || !readingID.MatchString(share.OwnerID) || share.SharedToID != userID ||
			share.MeetingID != docID || share.Permission != model.PermissionRead {
			return nil, ErrNotFound
		}
		pk, scope, permission, grant = model.PrefixUser+share.OwnerID, "shared", "read", readingHash(share)
		doc, err = s.repo.GetAccountDocument(ctx, pk, docID)
		if err != nil {
			return nil, err
		}
	}
	if doc == nil {
		return nil, ErrNotFound
	}
	if doc.PK != pk || doc.DocID != docID {
		return nil, ErrReadingData
	}
	return &documentReadingSource{doc, scope, permission, grant}, nil
}

func documentReadingRevision(source *documentReadingSource) string {
	doc := *source.doc
	// Public-link capabilities are neither output nor hashed into read identity.
	doc.PublicShareToken = ""
	return readingHash(doc)
}

func documentFileOwner(key string) (string, error) { return privateFileKeyOwner(key, "docs") }

func (s *DocumentReadingService) fileLinks(ctx context.Context, userID, accountID, docID string,
	source *documentReadingSource, section string, base readingObject) error {
	if s.files == nil || s.signer == nil {
		return ErrReadingData
	}
	var detail *model.AccountDocumentDetail
	var err error
	if accountID == "" {
		detail, err = s.files.GetUserDocument(ctx, userID, docID)
	} else {
		detail, err = s.files.GetAccountDocument(ctx, userID, accountID, docID)
	}
	if err != nil {
		return err
	}
	if detail == nil || detail.DocID != docID || detail.FileKey != source.doc.FileKey {
		return ErrStaleReadingCursor
	}
	owner, err := documentFileOwner(detail.FileKey)
	if err != nil {
		return err
	}
	// Personal/email-shared files must belong to the source partition's owner.
	// For account documents the canonical key is authoritative: another account
	// editor may have replaced it, so SourceUserID is intentionally not used.
	if accountID == "" {
		owner = strings.TrimPrefix(source.doc.PK, model.PrefixUser)
	}
	if err := validateFileKeyOwnership(owner, detail.FileKey); err != nil {
		return err
	}
	current, err := s.source(ctx, userID, accountID, docID)
	if err != nil {
		return err
	}
	if current.grant != source.grant || documentReadingRevision(current) != documentReadingRevision(source) {
		return ErrStaleReadingCursor
	}
	file := base["file"].(readingObject)
	base["urlExpiresInSeconds"] = int(PublicShareURLTTL.Seconds())
	if section != "preview" {
		link, err := s.signer.GeneratePresignedDownloadURLWithTTL(ctx, detail.FileKey, PublicShareURLTTL)
		if err != nil || link == "" {
			file["downloadStatus"] = "unavailable"
		} else {
			file["downloadStatus"], base["downloadUrl"] = "available", link
		}
	}
	if section != "download" {
		current, err = s.source(ctx, userID, accountID, docID)
		if err != nil {
			return err
		}
		if current.grant != source.grant || documentReadingRevision(current) != documentReadingRevision(source) {
			return ErrStaleReadingCursor
		}
		link, err := s.signer.GeneratePreviewPDFURLShortLived(ctx, detail.FileKey)
		if err != nil || link == "" {
			file["previewStatus"] = "unavailable"
		} else {
			file["previewStatus"], base["previewUrl"] = "available", link
		}
	}
	return nil
}

// Read sizes the encoded JSON before returning it. Every call re-resolves the
// source and grant; access changes and concurrent edits invalidate continuation.
func (s *DocumentReadingService) Read(ctx context.Context, userID, accountID, docID string, q url.Values) ([]byte, error) {
	if userID == "" {
		return nil, ErrForbidden
	}
	if !readingID.MatchString(docID) || (accountID != "" && !readingID.MatchString(accountID)) {
		return nil, ErrInvalidReadingOptions
	}
	o, err := parseDocumentReading(q)
	if err != nil {
		return nil, err
	}
	source, err := s.source(ctx, userID, accountID, docID)
	if err != nil {
		return nil, err
	}
	doc := source.doc
	if !utf8.ValidString(doc.Content) || len(doc.Content) > maxInlineDocBytes {
		return nil, ErrReadingData
	}
	revision := documentReadingRevision(source)
	if o.ExpectedRevision != "" && o.ExpectedRevision != revision {
		return nil, ErrStaleReadingCursor
	}
	binding := readingHash([]interface{}{1, "document", userID, accountID, docID, o.Section,
		source.scope, source.permission, source.grant, revision})
	text, mode, textStatus := doc.Content, "text", "available"
	if text == "" {
		textStatus = "empty"
	}
	if doc.FileKey != "" {
		text, mode, textStatus = "", "file", "not_available"
	} else if o.Section == "download" || o.Section == "preview" {
		return nil, ErrInvalidReadingOptions
	}
	if o.Section == "metadata" {
		text = ""
	}
	truncated := []string{}
	base := readingObject{
		"docId": docID, "accountId": accountID, "scope": source.scope, "section": o.Section,
		"permission": source.permission, "revision": revision, "mode": mode, "textStatus": textStatus,
		"title":      readingLimit(doc.Title, 256, "title", &truncated),
		"docType":    readingLimit(doc.DocType, 64, "docType", &truncated),
		"path":       readingLimit(doc.Path, 256, "path", &truncated),
		"updatedAt":  doc.UpdatedAt.Format(time.RFC3339Nano),
		"provenance": readingObject{"source": "saved_document", "field": "content", "currentAccessChecked": true},
	}
	if mode == "file" {
		base["file"] = readingObject{
			"name":     readingLimit(doc.FileName, 256, "file.name", &truncated),
			"mimeType": readingLimit(doc.MimeType, 128, "file.mimeType", &truncated), "size": doc.FileSize,
			"textExtracted": false,
		}
		if err := s.fileLinks(ctx, userID, accountID, docID, source, o.Section, base); err != nil {
			return nil, err
		}
	}
	base["metadataTruncated"] = truncated
	windows := []readingWindow{}
	if text != "" {
		windows = append(windows, readingWindow{end: utf8.RuneCountInString(text)})
	}
	render := func(chunks []readingChunk, page readingObject) readingObject {
		content := ""
		for _, chunk := range chunks {
			content += chunk.Text
		}
		base["content"], base["page"] = content, page
		return base
	}
	body, err := paginateReading(text, windows, o, binding, render)
	if errors.Is(err, ErrReadingData) && mode == "file" {
		// Links are never truncated. Reduce only explicitly marked metadata
		// previews, then defer the secondary link to its own authorized read.
		for _, field := range []string{"title", "docType", "path"} {
			base[field] = readingLimit(base[field].(string), 32, field, &truncated)
		}
		file := base["file"].(readingObject)
		for _, field := range []string{"name", "mimeType"} {
			file[field] = readingLimit(file[field].(string), 32, "file."+field, &truncated)
		}
		base["metadataTruncated"] = truncated
		body, err = paginateReading(text, windows, o, binding, render)
		if errors.Is(err, ErrReadingData) && o.Section != "preview" && base["previewUrl"] != nil {
			delete(base, "previewUrl")
			file["previewStatus"] = "deferred"
			next := readingObject{"section": "preview", "docId": docID, "expectedRevision": revision}
			if accountID != "" {
				next["accountId"] = accountID
			}
			base["previewRead"] = next
			body, err = paginateReading(text, windows, o, binding, render)
		}
	}
	if err != nil {
		return nil, err
	}
	current, err := s.source(ctx, userID, accountID, docID)
	if err != nil {
		return nil, err
	}
	if current.scope != source.scope || current.permission != source.permission || current.grant != source.grant ||
		documentReadingRevision(current) != revision {
		return nil, ErrStaleReadingCursor
	}
	return body, nil
}
