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
)

type vaultReadingOptions struct {
	section, cursor, file string
	size                  int
}

func parseVaultReading(q url.Values) (vaultReadingOptions, error) {
	o := vaultReadingOptions{section: "manifest", size: 4000}
	for key, values := range q {
		if len(values) != 1 {
			return o, ErrInvalidReadingOptions
		}
		v := values[0]
		switch key {
		case "section":
			o.section = v
		case "fileId":
			if v == "" || len(v) > 2048 {
				return o, ErrInvalidReadingOptions
			}
			o.file = v
		case "cursor":
			if v == "" || len(v) > 2048 {
				return o, ErrInvalidReadingCursor
			}
			o.cursor = v
		case "pageSize":
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 8000 {
				return o, ErrInvalidReadingOptions
			}
			o.size = n
		default:
			return o, ErrInvalidReadingOptions
		}
	}
	if (o.section != "manifest" && o.section != "file") || (o.section == "file" && o.file == "") || (o.section == "manifest" && o.file != "") {
		return o, ErrInvalidReadingOptions
	}
	return o, nil
}
func encodeVaultCursor(c interface{}) (string, error) {
	raw, _ := json.Marshal(c)
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	if len(encoded) > 2048 {
		return "", ErrReadingData
	}
	return encoded, nil
}
func decodeVaultCursor(raw string, c interface{}) error {
	data, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil || json.Unmarshal(data, c) != nil {
		return ErrInvalidReadingCursor
	}
	canonical, _ := json.Marshal(c)
	if !bytes.Equal(data, canonical) {
		return ErrInvalidReadingCursor
	}
	return nil
}
func (s *VaultReadingService) Read(ctx context.Context, user string, q url.Values) ([]byte, error) {
	if user == "" {
		return nil, ErrForbidden
	}
	o, err := parseVaultReading(q)
	if err != nil {
		return nil, err
	}
	if o.section == "manifest" {
		return s.manifest(ctx, user, o)
	}
	return s.file(ctx, user, o)
}

type vaultManifestCursor struct {
	Version  int    `json:"v"`
	Binding  string `json:"b"`
	Phase    string `json:"p"`
	After    string `json:"k"`
	Offset   int    `json:"o"`
	Revision string `json:"r"`
	Account  string `json:"a"`
	Grant    string `json:"g"`
	NavAfter string `json:"n"`
	NavDone  bool   `json:"d"`
	Anchor   string `json:"h"`
}

func (s *VaultReadingService) checkManifestAnchor(ctx context.Context, user, raw string) error {
	if raw == "" {
		return nil
	}
	token, err := decodeVaultFile(raw, user)
	if err != nil {
		return err
	}
	source, err := s.source(ctx, user, token.Kind, token.Account, token.ID)
	if errors.Is(err, ErrForbidden) || errors.Is(err, ErrNotFound) {
		return ErrStaleReadingCursor
	}
	if err != nil {
		return err
	}
	if source.token != token {
		return ErrStaleReadingCursor
	}
	return nil
}
func (s *VaultReadingService) manifestPayload(ctx context.Context, user string, c vaultManifestCursor) (string, string, string, error) {
	ids := []string{}
	next := ""
	kind, account := "document", c.Account
	if c.Phase == "meetings" {
		kind, account = "meeting", ""
		page, err := s.repo.QueryMeetingCatalog(ctx, user, "owned", "", c.After, 10)
		if err != nil {
			return "", "", "", err
		}
		if page == nil || len(page.Rows) > 10 {
			return "", "", "", ErrReadingData
		}
		next = page.NextKey
		for _, row := range page.Rows {
			if row.OwnerID != user {
				return "", "", "", ErrReadingData
			}
			ids = append(ids, row.MeetingID)
		}
	} else {
		pk := model.PrefixUser + user
		if c.Phase == "account_docs" {
			pk = model.PrefixAccount + c.Account
		} else if c.Phase != "personal_docs" {
			return "", "", "", ErrInvalidReadingCursor
		}
		page, err := s.repo.QueryDocumentCandidates(ctx, pk, model.PrefixDoc, c.After, 10)
		if err != nil {
			return "", "", "", err
		}
		if page == nil || len(page.Rows) > 10 {
			return "", "", "", ErrReadingData
		}
		next = page.NextKey
		for _, row := range page.Rows {
			if !strings.HasPrefix(row.SK, model.PrefixDoc) {
				return "", "", "", ErrReadingData
			}
			ids = append(ids, strings.TrimPrefix(row.SK, model.PrefixDoc))
		}
	}
	rows := []readingObject{}
	anchor := c.Anchor
	for _, id := range ids {
		if !readingID.MatchString(id) {
			return "", "", "", ErrReadingData
		}
		source, err := s.source(ctx, user, kind, account, id)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return "", "", "", err
		}
		mode, title := "text", ""
		if source.meeting != nil {
			title = source.meeting.Title
		} else {
			title = source.doc.Title
			if source.doc.FileKey != "" {
				mode = "non_text"
			}
		}
		token := encodeVaultFile(source.token)
		rows = append(rows, readingObject{"fileId": token, "path": source.path, "kind": kind, "title": title, "mode": mode, "sourceMetadataRevision": source.token.Revision})
		anchor = token
	}
	raw, err := json.Marshal(rows)
	return string(raw), next, anchor, err
}
func (s *VaultReadingService) manifest(ctx context.Context, user string, o vaultReadingOptions) ([]byte, error) {
	binding := compactReadingHash([]string{"vault-manifest-v1", user})
	c := vaultManifestCursor{Version: 1, Binding: binding, Phase: "meetings"}
	if o.cursor != "" {
		if err := decodeVaultCursor(o.cursor, &c); err != nil {
			return nil, err
		}
		if c.Version != 1 || c.Offset < 0 || (c.Offset > 0 && c.Revision == "") {
			return nil, ErrInvalidReadingCursor
		}
		if c.Binding != binding {
			return nil, ErrStaleReadingCursor
		}
	}
	if err := s.checkManifestAnchor(ctx, user, c.Anchor); err != nil {
		return nil, err
	}
	if c.Phase == "accounts" {
		if c.Offset != 0 || c.Revision != "" || c.After != "" || (c.NavAfter != "" && !readingID.MatchString(c.NavAfter)) {
			return nil, ErrInvalidReadingCursor
		}
		page, err := queryReadableAccountCandidates(ctx, s.repo, user, c.NavAfter, 1)
		if err != nil {
			return nil, err
		}
		if page == nil || len(page.IDs) > 1 {
			return nil, ErrReadingData
		}
		c.NavAfter, c.NavDone = page.NextID, page.NextID == ""
		complete := c.NavDone
		if len(page.IDs) > 0 {
			account := page.IDs[0]
			access, err := resolveAccountReadGrant(ctx, s.repo, account, user)
			if err != nil {
				return nil, err
			}
			if access != nil {
				c.Account, c.Grant, c.Phase = account, access.fingerprint(), "account_docs"
				complete = false
			}
		}
		var next interface{}
		if !complete {
			next, err = encodeVaultCursor(c)
			if err != nil {
				return nil, err
			}
		}
		return json.Marshal(readingObject{"kind": "vault_manifest", "phase": "discovery", "discoveryPending": true, "page": readingObject{"complete": complete, "nextCursor": next}})
	}
	if c.Phase != "meetings" && c.Phase != "personal_docs" && c.Phase != "account_docs" {
		return nil, ErrInvalidReadingCursor
	}
	if c.Phase != "account_docs" && c.Account != "" {
		return nil, ErrInvalidReadingCursor
	}
	checkGrant := func() error {
		if c.Phase != "account_docs" {
			return nil
		}
		if !readingID.MatchString(c.Account) {
			return ErrInvalidReadingCursor
		}
		access, err := resolveAccountReadGrant(ctx, s.repo, c.Account, user)
		if err != nil {
			return err
		}
		if access == nil || access.fingerprint() != c.Grant {
			return ErrStaleReadingCursor
		}
		return nil
	}
	if err := checkGrant(); err != nil {
		return nil, err
	}
	text, nextKey, anchor, err := s.manifestPayload(ctx, user, c)
	if err != nil {
		return nil, err
	}
	group := compactReadingHash([]string{c.Phase, c.After, c.Grant, text, nextKey})
	if c.Revision != "" && c.Revision != group {
		return nil, ErrStaleReadingCursor
	}
	body, err := renderCollectionReading("vault_manifest", c.Phase, text, binding, group, c.Offset, o.size, readingObject{"unionKey": "path"}, func(done bool, end int) (string, bool, error) {
		next := c
		next.Anchor = anchor
		if !done {
			next.Offset, next.Revision = end, group
		} else {
			next.After, next.Offset, next.Revision = nextKey, 0, ""
			if nextKey == "" {
				switch c.Phase {
				case "meetings":
					next.Phase = "personal_docs"
				case "personal_docs":
					next.Phase = "accounts"
				case "account_docs":
					if c.NavDone {
						return "", true, nil
					}
					next.Phase, next.Account, next.Grant = "accounts", "", ""
				}
			}
		}
		encoded, err := encodeVaultCursor(next)
		return encoded, false, err
	})
	if err != nil {
		return nil, err
	}
	fresh, freshNext, freshAnchor, err := s.manifestPayload(ctx, user, c)
	if err != nil {
		return nil, err
	}
	if fresh != text || freshNext != nextKey || freshAnchor != anchor {
		return nil, ErrStaleReadingCursor
	}
	if err := checkGrant(); err != nil {
		return nil, err
	}
	if err := s.checkManifestAnchor(ctx, user, anchor); err != nil {
		return nil, err
	}
	return body, nil
}

type vaultFileCursor struct {
	Version  int    `json:"v"`
	Binding  string `json:"b"`
	Phase    string `json:"p"`
	After    string `json:"a"`
	Offset   int    `json:"o"`
	Revision string `json:"r"`
}

func (s *VaultReadingService) file(ctx context.Context, user string, o vaultReadingOptions) ([]byte, error) {
	token, err := decodeVaultFile(o.file, user)
	if err != nil {
		return nil, err
	}
	source, err := s.source(ctx, user, token.Kind, token.Account, token.ID)
	if err != nil {
		return nil, err
	}
	if source.token != token {
		return nil, ErrStaleReadingCursor
	}
	if source.doc != nil && source.doc.FileKey != "" {
		if o.cursor != "" {
			return nil, ErrInvalidReadingCursor
		}
		fresh, err := s.source(ctx, user, token.Kind, token.Account, token.ID)
		if err != nil {
			return nil, err
		}
		if fresh.token != token {
			return nil, ErrStaleReadingCursor
		}
		read := readingObject{"docId": token.ID}
		if token.Account != "" {
			read["accountId"] = token.Account
		}
		return json.Marshal(readingObject{"kind": "vault_file", "fileId": o.file, "path": source.path, "mode": "non_text", "textStatus": "not_available", "documentRead": read, "page": readingObject{"complete": true, "nextCursor": nil}})
	}
	markdown, err := s.body(ctx, source)
	if err != nil {
		return nil, err
	}
	binding := compactReadingHash([]string{"vault-file-v1", o.file, markdown})
	c := vaultFileCursor{Version: 1, Binding: binding, Phase: "body"}
	if o.cursor != "" {
		if err := decodeVaultCursor(o.cursor, &c); err != nil {
			return nil, err
		}
		if c.Version != 1 || c.Offset < 0 || (c.Offset > 0 && c.Revision == "") {
			return nil, ErrInvalidReadingCursor
		}
		if c.Binding != binding {
			return nil, ErrStaleReadingCursor
		}
	}
	if c.Phase != "body" && c.Phase != "attachments" {
		return nil, ErrInvalidReadingCursor
	}
	if c.Phase == "body" && c.After != "" || c.Phase == "attachments" && source.meeting == nil {
		return nil, ErrInvalidReadingCursor
	}
	payload := func() (string, string, error) {
		if c.Phase == "body" {
			return markdown, "", nil
		}
		page, err := s.repo.QueryVaultAttachments(ctx, token.ID, c.After)
		if err != nil {
			return "", "", err
		}
		if page == nil || len(page.Rows) > 10 {
			return "", "", ErrReadingData
		}
		return renderVaultAttachmentGroup(page.Rows, c.After == "", page.NextKey == ""), page.NextKey, nil
	}
	text, nextKey, err := payload()
	if err != nil {
		return nil, err
	}
	group := compactReadingHash([]string{c.Phase, c.After, text, nextKey})
	if c.Revision != "" && c.Revision != group {
		return nil, ErrStaleReadingCursor
	}
	inner := ReadingOptions{PageSize: o.size, MaxBytes: 10000}
	innerBinding := readingHash([]string{binding, group})
	if c.Offset > 0 {
		raw, _ := json.Marshal(readingCursor{1, innerBinding, c.Offset})
		inner.Cursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	windows := []readingWindow{}
	if text != "" {
		windows = append(windows, readingWindow{end: utf8.RuneCountInString(text)})
	}
	var renderErr error
	body, err := paginateReading(text, windows, inner, innerBinding, func(chunks []readingChunk, fragment readingObject) readingObject {
		part := ""
		for _, chunk := range chunks {
			part += chunk.Text
		}
		done := fragment["complete"].(bool)
		complete := done && (source.doc != nil || (c.Phase == "attachments" && nextKey == ""))
		next := c
		var cursor interface{}
		if !complete {
			if !done {
				next.Offset, next.Revision = fragment["endOffset"].(int), group
			} else {
				next.Offset, next.Revision, next.After = 0, "", nextKey
				if c.Phase == "body" {
					next.Phase = "attachments"
				}
			}
			cursor, renderErr = encodeVaultCursor(next)
		}
		delete(fragment, "nextCursor")
		return readingObject{"kind": "vault_file", "fileId": o.file, "path": source.path, "mode": "text", "phase": c.Phase, "consistency": "current_source_and_group", "sourceKind": token.Kind, "sourceRevision": readingHash([]string{token.Revision, markdown}), "revision": readingHash(group), "markdown": part, "fragment": fragment, "page": readingObject{"complete": complete, "nextCursor": cursor}}
	})
	if err != nil {
		return nil, err
	}
	if renderErr != nil {
		return nil, renderErr
	}
	fresh, err := s.source(ctx, user, token.Kind, token.Account, token.ID)
	if err != nil {
		return nil, err
	}
	if fresh.token != token {
		return nil, ErrStaleReadingCursor
	}
	freshBody, err := s.body(ctx, fresh)
	if err != nil {
		return nil, err
	}
	if freshBody != markdown {
		return nil, ErrStaleReadingCursor
	}
	freshText, freshNext, err := payload()
	if err != nil {
		return nil, err
	}
	if freshText != text || freshNext != nextKey {
		return nil, ErrStaleReadingCursor
	}
	latest, err := s.source(ctx, user, token.Kind, token.Account, token.ID)
	if err != nil {
		return nil, err
	}
	if latest.token != token {
		return nil, ErrStaleReadingCursor
	}
	return body, nil
}
