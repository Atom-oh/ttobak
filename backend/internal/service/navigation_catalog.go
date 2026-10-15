package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"sort"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type NavigationCatalogRepository interface {
	QueryNavigationCandidates(context.Context, string, string, string, string, int) (*repository.NavigationCandidatePage, error)
	GetAccount(context.Context, string) (*model.Account, error)
	GetMember(context.Context, string, string) (*model.AccountMember, error)
	GetProjectCatalogMetadata(context.Context, string) (*model.Project, error)
	GetProjectMember(context.Context, string, string) (*model.ProjectMember, error)
}
type NavigationCatalogService struct{ repo NavigationCatalogRepository }

func NewNavigationCatalogService(repo NavigationCatalogRepository) *NavigationCatalogService {
	return &NavigationCatalogService{repo: repo}
}

type navigationAnchor struct {
	ID       string `json:"i"`
	Via      string `json:"v"`
	Account  string `json:"a,omitempty"`
	Revision string `json:"r"`
	Grant    string `json:"g"`
}
type navigationCursor struct {
	Version      int              `json:"v"`
	Binding      string           `json:"b"`
	Phase        string           `json:"p"`
	After        string           `json:"s,omitempty"`
	Account      string           `json:"a,omitempty"`
	AccountGrant string           `json:"g,omitempty"`
	AccountDone  bool             `json:"d,omitempty"`
	LastAccount  bool             `json:"l,omitempty"`
	Emitted      []string         `json:"e,omitempty"`
	Overlap      bool             `json:"x,omitempty"`
	Anchor       navigationAnchor `json:"n"`
}

func decodeNavigationCursor(raw string) (navigationCursor, error) {
	var c navigationCursor
	data, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil || json.Unmarshal(data, &c) != nil || c.Version != 1 {
		return c, ErrInvalidReadingCursor
	}
	canonical, _ := json.Marshal(c)
	if !bytes.Equal(data, canonical) {
		return c, ErrInvalidReadingCursor
	}
	for _, id := range []string{c.After, c.Account, c.Anchor.ID, c.Anchor.Account} {
		if id != "" && !readingID.MatchString(id) {
			return c, ErrInvalidReadingCursor
		}
	}
	if c.Phase != "accounts" && c.Phase != "owned" && c.Phase != "direct" {
		return c, ErrInvalidReadingCursor
	}
	if len(c.Emitted) > 32 {
		return c, ErrInvalidReadingCursor
	}
	seen := map[string]bool{}
	for _, id := range c.Emitted {
		if !readingID.MatchString(id) || seen[id] {
			return c, ErrInvalidReadingCursor
		}
		seen[id] = true
	}
	if c.Anchor.ID == "" && c.Anchor != (navigationAnchor{}) {
		return c, ErrInvalidReadingCursor
	}
	return c, nil
}

type navigationItem struct {
	row    readingObject
	anchor navigationAnchor
}

func (s *NavigationCatalogService) account(ctx context.Context, user, id string) (*navigationItem, error) {
	member, err := s.repo.GetMember(ctx, id, user)
	if err != nil {
		return nil, err
	}
	if member == nil || member.AccountID != id || member.UserID != user {
		return nil, ErrNotFound
	}
	account, err := s.repo.GetAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, ErrNotFound
	}
	if account.AccountID != id {
		return nil, ErrReadingData
	}
	truncated := []string{}
	revision, grant := readingHash(account), readingHash(member)
	row := readingObject{"accountId": id, "name": readingLimit(account.Name, 128, "name", &truncated),
		"parentAccountId": readingLimit(account.ParentAccountID, 128, "parentAccountId", &truncated),
		"role":            readingLimit(member.Role, 64, "role", &truncated), "metadataRevision": revision}
	row["metadataTruncated"] = truncated
	return &navigationItem{row, navigationAnchor{ID: id, Via: "account", Revision: revision, Grant: grant}}, nil
}

func normalizedProjectMetadata(project *model.Project) (*model.Project, error) {
	if project == nil {
		return nil, ErrNotFound
	}
	copy := *project
	copy.AccountIDs = append([]string{}, project.AccountIDs...)
	sort.Strings(copy.AccountIDs)
	for _, id := range copy.AccountIDs {
		if !readingID.MatchString(id) {
			return nil, ErrReadingData
		}
	}
	return &copy, nil
}

func (s *NavigationCatalogService) project(ctx context.Context, user, id, via, account string) (*navigationItem, *model.Project, error) {
	found, err := s.repo.GetProjectCatalogMetadata(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	project, err := normalizedProjectMetadata(found)
	if err != nil {
		return nil, nil, err
	}
	if project.ProjectID != id || project.PK != model.PrefixProject+id || project.SK != model.SKProjectConfig {
		return nil, nil, ErrReadingData
	}
	grant := ""
	switch via {
	case "owned":
		if project.OwnerUserID != user {
			return nil, nil, ErrNotFound
		}
		grant = readingHash(user)
	case "direct":
		member, err := s.repo.GetProjectMember(ctx, id, user)
		if err != nil {
			return nil, nil, err
		}
		if member == nil || member.ProjectID != id || member.UserID != user {
			return nil, nil, ErrNotFound
		}
		grant = readingHash(member)
	case "accounts":
		member, err := s.repo.GetMember(ctx, account, user)
		if err != nil {
			return nil, nil, err
		}
		if member == nil || member.AccountID != account || member.UserID != user || !contains(project.AccountIDs, account) {
			return nil, nil, ErrNotFound
		}
		grant = readingHash(member)
	default:
		return nil, nil, ErrInvalidReadingCursor
	}
	truncated := []string{}
	revision := readingHash(project)
	row := readingObject{"projectId": id, "name": readingLimit(project.Name, 128, "name", &truncated),
		"stage":       readingLimit(project.Stage, 64, "stage", &truncated),
		"sfdcOpptyId": readingLimit(project.SfdcOpptyID, 128, "sfdcOpptyId", &truncated),
		"accessVia":   via, "metadataRevision": revision}
	if via == "accounts" {
		row["accountId"] = account
	}
	row["metadataTruncated"] = truncated
	return &navigationItem{row, navigationAnchor{ID: id, Via: via, Account: account, Revision: revision, Grant: grant}}, project, nil
}

func (s *NavigationCatalogService) revalidate(ctx context.Context, user, kind string, anchor navigationAnchor) error {
	if anchor.ID == "" {
		return nil
	}
	var item *navigationItem
	var err error
	if kind == "accounts" {
		item, err = s.account(ctx, user, anchor.ID)
	} else {
		item, _, err = s.project(ctx, user, anchor.ID, anchor.Via, anchor.Account)
	}
	if err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) {
			return ErrStaleReadingCursor
		}
		return err
	}
	if item.anchor != anchor {
		return ErrStaleReadingCursor
	}
	return nil
}

func (s *NavigationCatalogService) Read(ctx context.Context, user, kind string, q url.Values) ([]byte, error) {
	if !readingID.MatchString(user) {
		return nil, ErrForbidden
	}
	if kind != "accounts" && kind != "projects" {
		return nil, ErrInvalidReadingOptions
	}
	for key := range q {
		if key != "pageSize" && key != "cursor" {
			return nil, ErrInvalidReadingOptions
		}
	}
	o, err := parseDocumentCatalog(q)
	if err != nil {
		return nil, err
	}
	binding := readingHash([]string{"navigation-v1", user, kind})
	c := navigationCursor{Version: 1, Binding: binding, Phase: "owned"}
	if kind == "accounts" {
		c.Phase = "accounts"
	}
	if o.cursor != "" {
		c, err = decodeNavigationCursor(o.cursor)
		if err != nil {
			return nil, err
		}
		if c.Binding != binding || (kind == "accounts" && c.Phase != "accounts") {
			return nil, ErrStaleReadingCursor
		}
		if kind == "accounts" && (c.Account != "" || c.AccountGrant != "" || c.AccountDone ||
			c.LastAccount || len(c.Emitted) != 0 || c.Overlap) {
			return nil, ErrInvalidReadingCursor
		}
		if err := s.revalidate(ctx, user, kind, c.Anchor); err != nil {
			return nil, err
		}
	}
	items, dependencies := []readingObject{}, []navigationAnchor{}
	render := func(more bool) ([]byte, error) {
		var next interface{}
		if more {
			for {
				data, _ := json.Marshal(c)
				cursor := base64.RawURLEncoding.EncodeToString(data)
				if len(cursor) <= 2048 {
					next = cursor
					break
				}
				if len(c.Emitted) == 0 {
					return nil, ErrReadingData
				}
				c.Emitted = c.Emitted[:len(c.Emitted)-1]
				c.Overlap = true
			}
		}
		result := readingObject{kind: items, "consistency": "current_per_item",
			"page": readingObject{"unit": "items", "returned": len(items), "complete": !more, "nextCursor": next}}
		if kind == "projects" {
			result["overlapPossible"], result["unionKey"] = c.Overlap, "projectId"
		}
		return readingJSON(result)
	}
	appendItem := func(item *navigationItem, before navigationCursor) (bool, error) {
		c.Anchor = item.anchor
		if kind == "projects" {
			if len(c.Emitted) < 32 {
				c.Emitted = append(c.Emitted, item.anchor.ID)
			} else {
				c.Overlap = true
			}
		}
		items = append(items, item.row)
		body, err := render(true)
		if err != nil {
			return false, err
		}
		if len(body) > 10000 {
			items, c = items[:len(items)-1], before
			return false, nil
		}
		dependencies = append(dependencies, item.anchor)
		return true, nil
	}
	finish := func(more bool) ([]byte, error) {
		for _, anchor := range dependencies {
			if err := s.revalidate(ctx, user, kind, anchor); err != nil {
				return nil, err
			}
		}
		if kind == "projects" && c.Phase == "accounts" && c.Account != "" && c.AccountGrant != "" {
			member, err := s.repo.GetMember(ctx, c.Account, user)
			if err != nil {
				return nil, err
			}
			if member == nil || readingHash(member) != c.AccountGrant {
				return nil, ErrStaleReadingCursor
			}
		}
		body, err := render(more)
		if err != nil || len(body) > 10000 {
			return nil, ErrReadingData
		}
		return body, nil
	}
	// A global account-index page is enough for the account catalog. Projects
	// visit one account at a time and keep that position in the server cursor.
	if kind == "projects" && c.Phase == "accounts" {
		if c.AccountDone || c.Account == "" {
			if c.AccountDone && c.LastAccount {
				return finish(false)
			}
			page, err := s.repo.QueryNavigationCandidates(ctx, user, "accounts", "", c.Account, 1)
			if err != nil {
				return nil, err
			}
			if page == nil || len(page.IDs) > 1 {
				return nil, ErrReadingData
			}
			if len(page.IDs) == 0 {
				if page.NextID == "" {
					return finish(false)
				}
				if !readingID.MatchString(page.NextID) || page.NextID == c.Account {
					return nil, ErrReadingData
				}
				c.Account, c.AccountDone, c.AccountGrant = page.NextID, true, ""
				return finish(true)
			}
			if !readingID.MatchString(page.IDs[0]) {
				return nil, ErrReadingData
			}
			c.Account, c.AccountDone, c.LastAccount, c.After = page.IDs[0], false, page.NextID == "", ""
			c.AccountGrant = ""
			member, err := s.repo.GetMember(ctx, c.Account, user)
			if err != nil {
				return nil, err
			}
			if member == nil || member.UserID != user || member.AccountID != c.Account {
				c.AccountDone = true
				return finish(!c.LastAccount)
			}
			c.AccountGrant = readingHash(member)
		}
		member, err := s.repo.GetMember(ctx, c.Account, user)
		if err != nil {
			return nil, err
		}
		if member == nil || readingHash(member) != c.AccountGrant {
			return nil, ErrStaleReadingCursor
		}
	}
	queryKind, account := c.Phase, ""
	if kind == "projects" && c.Phase == "accounts" {
		queryKind, account = "account-projects", c.Account
	}
	queryAfter := c.After
	page, err := s.repo.QueryNavigationCandidates(ctx, user, queryKind, account, c.After, o.pageSize)
	if err != nil {
		return nil, err
	}
	if page == nil || len(page.IDs) > o.pageSize {
		return nil, ErrReadingData
	}
	if page.NextID != "" && page.NextID == queryAfter {
		return nil, ErrReadingData
	}
	processed := 0
	for _, id := range page.IDs {
		if !readingID.MatchString(id) || id <= c.After {
			return nil, ErrReadingData
		}
		if kind == "projects" && contains(c.Emitted, id) {
			c.After = id
			processed++
			continue
		}
		before := c
		var item *navigationItem
		if kind == "accounts" {
			item, err = s.account(ctx, user, id)
		} else {
			item, _, err = s.project(ctx, user, id, c.Phase, c.Account)
		}
		if err != nil {
			if !errors.Is(err, ErrNotFound) {
				return nil, err
			}
			c.After = id
			processed++
			continue
		}
		c.After = id
		added, err := appendItem(item, before)
		if err != nil {
			return nil, err
		}
		if !added {
			if processed == 0 {
				return nil, ErrReadingData
			}
			return finish(true)
		}
		processed++
	}
	if page.NextID != "" {
		if !readingID.MatchString(page.NextID) {
			return nil, ErrReadingData
		}
		c.After = page.NextID
		return finish(true)
	}
	if kind == "accounts" {
		return finish(false)
	}
	switch c.Phase {
	case "owned":
		c.Phase, c.After = "direct", ""
	case "direct":
		c.Phase, c.After, c.AccountDone = "accounts", "", true
	case "accounts":
		c.AccountDone, c.After = true, ""
		if c.LastAccount {
			return finish(false)
		}
	}
	return finish(true)
}
