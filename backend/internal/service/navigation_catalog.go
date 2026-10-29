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
	ID           string `json:"i"`
	Via          string `json:"v"`
	Account      string `json:"a,omitempty"`
	Revision     string `json:"r"`
	Grant        string `json:"g"`
	GrantKind    string `json:"k,omitempty"`
	GrantAccount string `json:"t,omitempty"`
	RouteGrant   string `json:"u,omitempty"`
}
type navigationProjectScan struct {
	ID       string `json:"i"`
	Revision string `json:"r"`
	Offset   int    `json:"s"`
}
type navigationCursor struct {
	Discovery    *accountDiscoveryCursor `json:"z,omitempty"`
	ProjectScan  *navigationProjectScan  `json:"j,omitempty"`
	Version      int                     `json:"v"`
	Binding      string                  `json:"b"`
	Phase        string                  `json:"p"`
	After        string                  `json:"s,omitempty"`
	Account      string                  `json:"a,omitempty"`
	AccountGrant string                  `json:"g,omitempty"`
	AccountDone  bool                    `json:"d,omitempty"`
	LastAccount  bool                    `json:"l,omitempty"`
	Emitted      []string                `json:"e,omitempty"`
	Overlap      bool                    `json:"x,omitempty"`
	Anchor       navigationAnchor        `json:"n"`
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
	for _, id := range []string{c.After, c.Account, c.Anchor.ID, c.Anchor.Account, c.Anchor.GrantAccount} {
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
	if c.ProjectScan != nil && (c.Phase != "accounts" || c.Account == "" || c.AccountDone ||
		!readingID.MatchString(c.ProjectScan.ID) || c.ProjectScan.ID <= c.After ||
		c.ProjectScan.Revision == "" || c.ProjectScan.Offset <= 0 || c.ProjectScan.Offset%50 != 0) {
		return c, ErrInvalidReadingCursor
	}
	return c, nil
}

type navigationItem struct {
	row         readingObject
	anchor      navigationAnchor
	accountScan bool
}

func (s *NavigationCatalogService) account(ctx context.Context, user, id string) (*navigationItem, error) {
	access, err := resolveAccountReadGrant(ctx, s.repo, id, user)
	if err != nil {
		return nil, err
	}
	if access == nil || (!access.inherited() && (access.Member.AccountID != id || access.Member.UserID != user)) {
		return nil, ErrNotFound
	}
	member := access.Member
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
	revision, grant := readingHash(account), access.fingerprint()
	row := readingObject{"accountId": id, "name": readingLimit(account.Name, 128, "name", &truncated),
		"parentAccountId": readingLimit(account.ParentAccountID, 128, "parentAccountId", &truncated),
		"role":            readingLimit(member.Role, 64, "role", &truncated), "metadataRevision": revision}
	row["metadataTruncated"] = truncated
	row["accessPermission"] = access.permission()
	if access.inherited() {
		row["role"] = ""
		row["inheritedFromAccountId"] = access.InheritedFromAccountID
	}
	return &navigationItem{row: row, anchor: navigationAnchor{ID: id, Via: "account", Revision: revision, Grant: grant}}, nil
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

func (s *NavigationCatalogService) projectMetadata(ctx context.Context, id string) (*model.Project, error) {
	found, err := s.repo.GetProjectCatalogMetadata(ctx, id)
	if err != nil {
		return nil, err
	}
	project, err := normalizedProjectMetadata(found)
	if err != nil {
		return nil, err
	}
	if project.ProjectID != id || project.PK != model.PrefixProject+id || project.SK != model.SKProjectConfig {
		return nil, ErrReadingData
	}
	return project, nil
}

func (s *NavigationCatalogService) project(ctx context.Context, user, id, via, account string) (*navigationItem, *model.Project, error) {
	var scan *navigationProjectScan
	return s.projectPage(ctx, user, id, via, account, &scan)
}

// One catalog request visits at most one inherited project's direct-account
// search batch. Nothing is projected until all possible direct sources have
// been checked, or a current independent grant has been found.
func (s *NavigationCatalogService) projectPage(ctx context.Context, user, id, via, account string, scan **navigationProjectScan) (*navigationItem, *model.Project, error) {
	project, err := s.projectMetadata(ctx, id)
	if err != nil {
		if *scan != nil && errors.Is(err, ErrNotFound) {
			return nil, nil, ErrStaleReadingCursor
		}
		return nil, nil, err
	}
	revision := readingHash(project)
	if *scan != nil && ((*scan).ID != id || (*scan).Revision != revision ||
		(*scan).Offset >= len(project.AccountIDs)) {
		return nil, nil, ErrStaleReadingCursor
	}
	anchor := navigationAnchor{ID: id, Via: via, Account: account, Revision: revision}
	var accountAccess *accountReadGrant
	scanned := false
	switch via {
	case "owned":
		if project.OwnerUserID != user {
			return nil, nil, ErrNotFound
		}
		anchor.Grant, anchor.GrantKind = readingHash(user), "owner"
	case "direct":
		member, err := s.repo.GetProjectMember(ctx, id, user)
		if err != nil {
			return nil, nil, err
		}
		if member == nil || member.ProjectID != id || member.UserID != user {
			return nil, nil, ErrNotFound
		}
		anchor.Grant, anchor.GrantKind = readingHash(member), "member"
	case "accounts":
		access, err := resolveAccountReadGrant(ctx, s.repo, account, user)
		if err != nil {
			return nil, nil, err
		}
		if !validAccountReadIdentity(access, account, user) || !contains(project.AccountIDs, account) {
			if *scan != nil {
				return nil, nil, ErrStaleReadingCursor
			}
			return nil, nil, ErrNotFound
		}
		anchor.Grant, anchor.RouteGrant, anchor.GrantKind = access.fingerprint(), access.fingerprint(), "route"
		accountAccess = access
		// Discovery via an inherited account must preserve an independent
		// current project owner/member or direct linked-account grant.
		if access.inherited() {
			if project.OwnerUserID == user {
				anchor.Grant, anchor.GrantKind = readingHash(user), "owner"
				accountAccess = nil
			} else {
				member, err := s.repo.GetProjectMember(ctx, id, user)
				if err != nil {
					return nil, nil, err
				}
				if member != nil && member.ProjectID == id && member.UserID == user {
					anchor.Grant, anchor.GrantKind = readingHash(member), "member"
					accountAccess = nil
				} else {
					scanned = true
					start := 0
					if *scan != nil {
						start = (*scan).Offset
					}
					end := start + 50
					if end > len(project.AccountIDs) {
						end = len(project.AccountIDs)
					}
					for _, linked := range project.AccountIDs[start:end] {
						direct, err := s.repo.GetMember(ctx, linked, user)
						if err != nil {
							return nil, nil, err
						}
						if direct != nil && direct.AccountID == linked && direct.UserID == user {
							anchor.Grant = readingHash([]interface{}{linked, direct, direct.SummaryPrivacyVersion})
							anchor.GrantKind, anchor.GrantAccount = "account", linked
							accountAccess = nil
							break
						}
					}
					if accountAccess != nil && end < len(project.AccountIDs) {
						*scan = &navigationProjectScan{ID: id, Revision: revision, Offset: end}
						return nil, project, nil
					}
				}
			}
		}
	default:
		return nil, nil, ErrInvalidReadingCursor
	}
	*scan = nil
	truncated := []string{}
	row := readingObject{"projectId": id, "name": readingLimit(project.Name, 128, "name", &truncated),
		"stage":       readingLimit(project.Stage, 64, "stage", &truncated),
		"sfdcOpptyId": readingLimit(project.SfdcOpptyID, 128, "sfdcOpptyId", &truncated),
		"accessVia":   via, "metadataRevision": revision}
	if via == "accounts" {
		row["accountId"] = account
		row["accessPermission"] = model.PermissionEdit
		if accountAccess != nil {
			row["accessPermission"] = accountAccess.permission()
		}
		if accountAccess != nil && accountAccess.inherited() {
			row["inheritedFromAccountId"] = accountAccess.InheritedFromAccountID
		}
	}
	row["metadataTruncated"] = truncated
	return &navigationItem{row: row, anchor: anchor, accountScan: scanned}, project, nil
}

func (s *NavigationCatalogService) revalidateProject(ctx context.Context, user string, anchor navigationAnchor) error {
	// The flag-off catalog's previous cursor shape remains readable. In that
	// mode an account route is always direct and needs no linked-account scan.
	if anchor.GrantKind == "" {
		switch anchor.Via {
		case "owned":
			anchor.GrantKind = "owner"
		case "direct":
			anchor.GrantKind = "member"
		case "accounts":
			anchor.GrantKind, anchor.RouteGrant = "route", anchor.Grant
		}
	}
	if (anchor.Via == "owned" && anchor.GrantKind != "owner") ||
		(anchor.Via == "direct" && anchor.GrantKind != "member") ||
		(anchor.GrantKind != "account" && anchor.GrantAccount != "") {
		return ErrInvalidReadingCursor
	}
	project, err := s.projectMetadata(ctx, anchor.ID)
	if err != nil {
		return err
	}
	if readingHash(project) != anchor.Revision {
		return ErrStaleReadingCursor
	}
	var route *accountReadGrant
	if anchor.Via == "accounts" {
		route, err = resolveAccountReadGrant(ctx, s.repo, anchor.Account, user)
		if err != nil {
			return err
		}
		if !validAccountReadIdentity(route, anchor.Account, user) ||
			!contains(project.AccountIDs, anchor.Account) || route.fingerprint() != anchor.RouteGrant {
			return ErrStaleReadingCursor
		}
	} else if anchor.Via != "owned" && anchor.Via != "direct" {
		return ErrInvalidReadingCursor
	}
	grant := ""
	switch anchor.GrantKind {
	case "owner":
		if project.OwnerUserID == user {
			grant = readingHash(user)
		}
	case "member":
		member, err := s.repo.GetProjectMember(ctx, anchor.ID, user)
		if err != nil {
			return err
		}
		if member != nil && member.ProjectID == anchor.ID && member.UserID == user {
			grant = readingHash(member)
		}
	case "route":
		if route == nil {
			return ErrInvalidReadingCursor
		}
		grant = route.fingerprint()
	case "account":
		if !contains(project.AccountIDs, anchor.GrantAccount) {
			return ErrStaleReadingCursor
		}
		member, err := s.repo.GetMember(ctx, anchor.GrantAccount, user)
		if err != nil {
			return err
		}
		if member != nil && member.AccountID == anchor.GrantAccount && member.UserID == user {
			grant = readingHash([]interface{}{anchor.GrantAccount, member, member.SummaryPrivacyVersion})
		}
	default:
		return ErrInvalidReadingCursor
	}
	if grant == "" || grant != anchor.Grant {
		return ErrStaleReadingCursor
	}
	return nil
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
		err = s.revalidateProject(ctx, user, anchor)
	}
	if err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) {
			return ErrStaleReadingCursor
		}
		return err
	}
	if kind == "accounts" && item.anchor != anchor {
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
	mode := "navigation-v1"
	if descendantReadsEnabled() {
		mode = "navigation-descendant-v2"
		if kind == "projects" {
			mode = "navigation-descendant-v3"
		}
	}
	binding := readingHash([]string{mode, user, kind})
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
			c.LastAccount || len(c.Emitted) != 0 || c.Overlap || c.ProjectScan != nil) {
			return nil, ErrInvalidReadingCursor
		}
		if c.ProjectScan != nil && (!descendantReadsEnabled() || c.AccountGrant == "") {
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
		if kind == "accounts" && descendantReadsEnabled() {
			result["overlapPossible"], result["unionKey"] = true, "accountId"
		}
		if kind == "projects" {
			result["overlapPossible"], result["unionKey"] = c.Overlap || descendantReadsEnabled(), "projectId"
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
			access, err := resolveAccountReadGrant(ctx, s.repo, c.Account, user)
			if err != nil {
				return nil, err
			}
			if access == nil || access.fingerprint() != c.AccountGrant {
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
			page, err := queryReadableAccountCandidates(ctx, s.repo, user, c.Account, 1, &c.Discovery)
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
				if !readingID.MatchString(page.NextID) || (!descendantReadsEnabled() && page.NextID == c.Account) {
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
			access, err := resolveAccountReadGrant(ctx, s.repo, c.Account, user)
			if err != nil {
				return nil, err
			}
			if access == nil || (!access.inherited() && (access.Member.UserID != user || access.Member.AccountID != c.Account)) {
				c.AccountDone = true
				return finish(!c.LastAccount)
			}
			c.AccountGrant = access.fingerprint()
		}
		access, err := resolveAccountReadGrant(ctx, s.repo, c.Account, user)
		if err != nil {
			return nil, err
		}
		if access == nil || access.fingerprint() != c.AccountGrant {
			return nil, ErrStaleReadingCursor
		}
	}
	if c.ProjectScan != nil {
		before := c
		item, _, err := s.projectPage(ctx, user, c.ProjectScan.ID, c.Phase, c.Account, &c.ProjectScan)
		if err != nil {
			return nil, err
		}
		if item != nil {
			c.After = item.anchor.ID
			added, err := appendItem(item, before)
			if err != nil {
				return nil, err
			}
			if !added {
				return nil, ErrReadingData
			}
		}
		return finish(true)
	}
	queryKind, account := c.Phase, ""
	if kind == "projects" && c.Phase == "accounts" {
		queryKind, account = "account-projects", c.Account
	}
	queryAfter := c.After
	var page *repository.NavigationCandidatePage
	if kind == "accounts" {
		page, err = queryReadableAccountCandidates(ctx, s.repo, user, c.After, o.pageSize, &c.Discovery)
	} else {
		page, err = s.repo.QueryNavigationCandidates(ctx, user, queryKind, account, c.After, o.pageSize)
	}
	if err != nil {
		return nil, err
	}
	if page == nil || len(page.IDs) > o.pageSize {
		return nil, ErrReadingData
	}
	if page.NextID != "" && !(kind == "accounts" && descendantReadsEnabled()) && page.NextID == queryAfter {
		return nil, ErrReadingData
	}
	processed := 0
	for _, id := range page.IDs {
		if !readingID.MatchString(id) || (!(kind == "accounts" && descendantReadsEnabled()) && id <= c.After) {
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
			item, _, err = s.projectPage(ctx, user, id, c.Phase, c.Account, &c.ProjectScan)
		}
		if err != nil {
			if !errors.Is(err, ErrNotFound) {
				return nil, err
			}
			c.After = id
			processed++
			continue
		}
		if item == nil {
			return finish(true)
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
		if item.accountScan {
			return finish(true)
		}
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
