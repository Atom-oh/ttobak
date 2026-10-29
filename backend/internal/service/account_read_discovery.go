package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

const maxReadableAccounts = 1000

type accountReadUserListRepo interface {
	ListAccountsForUser(context.Context, string) ([]model.AccountMember, error)
}
type accountReadNavigationRepo interface {
	QueryNavigationCandidates(context.Context, string, string, string, string, int) (*repository.NavigationCandidatePage, error)
}
type accountReadChildrenRepo interface {
	ListChildAccountIDsPage(context.Context, string, string, int32) ([]string, *string, error)
}

// listReadableAccountIDs validates discovery rows against live canonical grants.
// The compatibility collection has an explicit 1,000-account limit; oversized
// trees fail instead of presenting a silently incomplete collection. Hints are
// additional candidate roots and never grant access.
func listReadableAccountIDs(ctx context.Context, repo accountReadMembershipRepo, userID string, hints []string) ([]string, error) {
	candidates := append([]string{}, hints...)
	if nav, ok := repo.(accountReadNavigationRepo); ok {
		after := ""
		for pages := 0; ; pages++ {
			if pages >= 10000 {
				return nil, ErrReadingData
			}
			page, err := nav.QueryNavigationCandidates(ctx, userID, "accounts", "", after, 1)
			if err != nil {
				return nil, err
			}
			if page == nil || len(page.IDs) > 1 {
				return nil, ErrReadingData
			}
			for _, id := range page.IDs {
				if !readingID.MatchString(id) || id <= after {
					return nil, ErrReadingData
				}
				candidates = append(candidates, id)
			}
			if page.NextID == "" {
				break
			}
			if !readingID.MatchString(page.NextID) || page.NextID <= after {
				return nil, ErrReadingData
			}
			after = page.NextID
		}
	} else if userList, ok := repo.(accountReadUserListRepo); ok {
		members, err := userList.ListAccountsForUser(ctx, userID)
		if err != nil {
			return nil, err
		}
		for _, m := range members {
			if m.UserID == userID {
				candidates = append(candidates, m.AccountID)
			}
		}
	}
	seen := map[string]bool{}
	grants := map[string]*accountReadGrant{}
	queue := []string{}
	add := func(id string) error {
		if !readingID.MatchString(id) {
			return ErrReadingData
		}
		if seen[id] {
			return nil
		}
		seen[id] = true
		if len(seen) > 10000 {
			return ErrReadingData
		}
		g, err := resolveAccountReadGrant(ctx, repo, id, userID)
		if err != nil {
			return err
		}
		if g == nil {
			return nil
		}
		if hierarchy, ok := repo.(accountReadHierarchyRepo); ok {
			a, err := hierarchy.GetAccount(ctx, id)
			if err != nil {
				return err
			}
			if a == nil {
				return nil
			}
			if a.AccountID != id {
				return ErrReadingData
			}
		}
		if len(grants) >= maxReadableAccounts {
			return ErrReadingData
		}
		grants[id] = g
		queue = append(queue, id)
		return nil
	}
	for _, id := range candidates {
		if err := add(id); err != nil {
			return nil, err
		}
	}
	children, canDiscover := repo.(accountReadChildrenRepo)
	hierarchy, canValidate := repo.(accountReadHierarchyRepo)
	if descendantReadsEnabled() && canDiscover && canValidate {
		for pos := 0; pos < len(queue); pos++ {
			parent := queue[pos]
			cursor := ""
			cursors := map[string]bool{}
			for pages := 0; ; pages++ {
				if pages >= 10000 {
					return nil, ErrReadingData
				}
				ids, next, err := children.ListChildAccountIDsPage(ctx, parent, cursor, 100)
				if err != nil {
					return nil, err
				}
				if len(ids) > 100 {
					return nil, ErrReadingData
				}
				for _, id := range ids {
					if !readingID.MatchString(id) {
						return nil, ErrReadingData
					}
					a, err := hierarchy.GetAccount(ctx, id)
					if err != nil {
						return nil, err
					}
					if !canonicalReadAccount(a, id) || a.ParentAccountID != parent {
						continue
					}
					if err := add(id); err != nil {
						return nil, err
					}
				}
				if next == nil {
					break
				}
				if *next == "" || len(*next) > 2048 || *next == cursor || cursors[*next] {
					return nil, ErrReadingData
				}
				cursors[*next] = true
				cursor = *next
			}
		}
	}
	ids := make([]string, 0, len(grants))
	for id, g := range grants {
		valid, err := revalidateAccountReadGrant(ctx, repo, g, userID)
		if err != nil {
			return nil, err
		}
		if !valid {
			return nil, ErrStaleReadingCursor
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

type AccountListPage struct {
	Accounts        []model.AccountSummary `json:"accounts"`
	NextCursor      string                 `json:"nextCursor,omitempty"`
	OverlapPossible bool                   `json:"overlapPossible,omitempty"`
	UnionKey        string                 `json:"unionKey,omitempty"`
}

type accountListCursor struct {
	Version   int                     `json:"v"`
	Binding   string                  `json:"b"`
	After     string                  `json:"a"`
	Discovery *accountDiscoveryCursor `json:"z,omitempty"`
	Anchor    string                  `json:"i,omitempty"`
	Revision  string                  `json:"r,omitempty"`
}

func (s *AccountService) ListAccountsPage(ctx context.Context, user string, q url.Values) (*AccountListPage, error) {
	size := 50
	rawCursor := ""
	for key, values := range q {
		if len(values) != 1 {
			return nil, ErrInvalidReadingOptions
		}
		switch key {
		case "pageSize":
			n, err := strconv.Atoi(values[0])
			if err != nil || n < 1 || n > 100 {
				return nil, ErrInvalidReadingOptions
			}
			size = n
		case "cursor":
			rawCursor = values[0]
			if rawCursor == "" || len(rawCursor) > 2048 {
				return nil, ErrInvalidReadingCursor
			}
		default:
			return nil, ErrInvalidReadingOptions
		}
	}
	if descendantReadsEnabled() {
		return s.listAccountsDiscoveryPage(ctx, user, size, rawCursor)
	}
	accounts, err := s.ListAccounts(ctx, user)
	if err != nil {
		return nil, err
	}
	fingerprints := []string{}
	for _, account := range accounts {
		g, err := s.requireReadGrant(ctx, user, account.AccountID)
		if err != nil {
			return nil, err
		}
		fingerprints = append(fingerprints, g.fingerprint())
	}
	binding := readingHash([]interface{}{"account-list-v1", user, accounts, fingerprints})
	cursor := accountListCursor{Version: 1, Binding: binding}
	if rawCursor != "" {
		data, err := base64.RawURLEncoding.Strict().DecodeString(rawCursor)
		if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.Version != 1 || !readingID.MatchString(cursor.After) {
			return nil, ErrInvalidReadingCursor
		}
		canonical, _ := json.Marshal(cursor)
		if !bytes.Equal(data, canonical) {
			return nil, ErrInvalidReadingCursor
		}
		if cursor.Binding != binding {
			return nil, ErrStaleReadingCursor
		}
	}
	start := sort.Search(len(accounts), func(i int) bool { return accounts[i].AccountID > cursor.After })
	end := start + size
	if end > len(accounts) {
		end = len(accounts)
	}
	page := &AccountListPage{Accounts: accounts[start:end]}
	if end < len(accounts) {
		cursor.After = accounts[end-1].AccountID
		data, _ := json.Marshal(cursor)
		page.NextCursor = base64.RawURLEncoding.EncodeToString(data)
	}
	for i, account := range accounts {
		g, err := s.requireReadGrant(ctx, user, account.AccountID)
		if err != nil {
			return nil, err
		}
		if g.fingerprint() != fingerprints[i] {
			return nil, ErrStaleReadingCursor
		}
	}
	return page, nil
}

func (s *AccountService) listAccountsDiscoveryPage(ctx context.Context, user string, size int, rawCursor string) (*AccountListPage, error) {
	binding := readingHash([]interface{}{"account-list-v2", user, size})
	c := accountListCursor{Version: 2, Binding: binding, Discovery: &accountDiscoveryCursor{}}
	if rawCursor != "" {
		data, err := base64.RawURLEncoding.Strict().DecodeString(rawCursor)
		if err != nil || json.Unmarshal(data, &c) != nil || c.Version != 2 || c.Discovery == nil ||
			c.After != "" || (c.Anchor != "" && !readingID.MatchString(c.Anchor)) {
			return nil, ErrInvalidReadingCursor
		}
		canonical, _ := json.Marshal(c)
		if !bytes.Equal(data, canonical) {
			return nil, ErrInvalidReadingCursor
		}
		if c.Binding != binding {
			return nil, ErrStaleReadingCursor
		}
	}
	recheckAccount := func(id, revision string) error {
		if id == "" {
			if revision != "" {
				return ErrInvalidReadingCursor
			}
			return nil
		}
		g, err := resolveAccountReadGrant(ctx, s.repo, id, user)
		if err != nil {
			return err
		}
		a, err := s.repo.GetAccount(ctx, id)
		if err != nil {
			return err
		}
		if !validAccountReadIdentity(g, id, user) || a == nil || a.AccountID != id ||
			readingHash([]interface{}{a, g.fingerprint()}) != revision {
			return ErrStaleReadingCursor
		}
		return nil
	}
	if err := recheckAccount(c.Anchor, c.Revision); err != nil {
		return nil, err
	}
	page := &AccountListPage{Accounts: []model.AccountSummary{}, OverlapPossible: true, UnionKey: "accountId"}
	observed := [][2]string{}
	done := false
	// A fixed four-step budget is independent of total tree width. Batching
	// empty ascent/index pages also preserves the frontend's page-count bound.
	for steps := 0; steps < 4 && len(page.Accounts) < size && !done; steps++ {
		ids, complete, err := nextReadableAccountCandidate(ctx, s.repo, user, c.Discovery)
		if err != nil {
			return nil, err
		}
		done = complete
		for _, id := range ids {
			g, err := s.requireReadGrant(ctx, user, id)
			if err != nil {
				return nil, err
			}
			a, err := s.repo.GetAccount(ctx, id)
			if err != nil {
				return nil, err
			}
			if a == nil || a.AccountID != id {
				return nil, ErrStaleReadingCursor
			}
			role := g.Member.Role
			if g.inherited() {
				role = ""
			}
			page.Accounts = append(page.Accounts, model.AccountSummary{
				AccountID: id, ParentAccountID: a.ParentAccountID, Name: a.Name,
				Role: role, AccessPermission: g.permission(), InheritedFromAccountID: g.InheritedFromAccountID,
			})
			c.Anchor, c.Revision = id, readingHash([]interface{}{a, g.fingerprint()})
			observed = append(observed, [2]string{c.Anchor, c.Revision})
		}
	}
	for _, receipt := range observed {
		if err := recheckAccount(receipt[0], receipt[1]); err != nil {
			return nil, err
		}
	}
	if err := recheckAccount(c.Anchor, c.Revision); err != nil {
		return nil, err
	}
	if !done {
		raw, _ := json.Marshal(c)
		page.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
		if len(page.NextCursor) > 2048 {
			return nil, ErrReadingData
		}
	}
	return page, nil
}

// accountDiscoveryCursor is constant in size: depth-first traversal reconstructs
// its return path from current canonical META links rather than embedding a
// frontier proportional to the number of descendants. One step performs at most
// one index/child query and validates at most the 64-link hierarchy bound.
type accountDiscoveryCursor struct {
	DirectAfter string `json:"a,omitempty"`
	DirectDone  bool   `json:"d,omitempty"`
	Root        string `json:"r,omitempty"`
	RootProof   string `json:"g,omitempty"`
	Node        string `json:"n,omitempty"`
	NodeProof   string `json:"p,omitempty"`
	ChildAfter  string `json:"c,omitempty"`
	Done        bool   `json:"f,omitempty"`
}

func (c *accountDiscoveryCursor) validate() error {
	if c == nil {
		return ErrInvalidReadingCursor
	}
	for _, id := range []string{c.DirectAfter, c.Root, c.Node} {
		if id != "" && !readingID.MatchString(id) {
			return ErrInvalidReadingCursor
		}
	}
	if len(c.ChildAfter) > 2048 || len(c.RootProof) > 128 || len(c.NodeProof) > 128 {
		return ErrInvalidReadingCursor
	}
	if c.Root == "" && (c.Node != "" || c.RootProof != "" || c.NodeProof != "" || c.ChildAfter != "") {
		return ErrInvalidReadingCursor
	}
	if c.Root != "" && (c.Node == "" || c.RootProof == "" || c.NodeProof == "" || c.Done) {
		return ErrInvalidReadingCursor
	}
	if c.Done && !c.DirectDone {
		return ErrInvalidReadingCursor
	}
	return nil
}

// discoveryProof validates the entire current path back to the directly held
// root. Cursor-supplied nodes/references do not themselves authorize traversal.
func discoveryProof(ctx context.Context, repo accountReadMembershipRepo, user, root, node string) (string, string, string, error) {
	hierarchy, ok := repo.(accountReadHierarchyRepo)
	if !ok {
		return "", "", "", ErrReadingData
	}
	rootGrant, err := resolveAccountReadGrant(ctx, repo, root, user)
	if err != nil {
		return "", "", "", err
	}
	if !validAccountReadIdentity(rootGrant, root, user) || rootGrant.inherited() {
		return "", "", "", ErrStaleReadingCursor
	}
	path := []repository.AccountParentLink{}
	seen := map[string]bool{}
	parent := ""
	var rootAccount *model.Account
	for id := node; ; {
		if !readingID.MatchString(id) || seen[id] || len(path) > repository.AccountHierarchyMaxAncestors {
			return "", "", "", ErrStaleReadingCursor
		}
		seen[id] = true
		a, err := hierarchy.GetAccount(ctx, id)
		if err != nil {
			return "", "", "", err
		}
		// Legacy direct records retain their existing identity contract. A
		// descendant traversal always requires canonical records throughout.
		if a == nil || a.AccountID != id || (node != root && !canonicalReadAccount(a, id)) {
			return "", "", "", ErrStaleReadingCursor
		}
		if id == node {
			parent = a.ParentAccountID
		}
		path = append(path, repository.AccountParentLink{AccountID: id, ParentAccountID: a.ParentAccountID})
		if id == root {
			rootAccount = a
			break
		}
		id = a.ParentAccountID
	}
	grant, err := resolveAccountReadGrant(ctx, repo, node, user)
	if err != nil {
		return "", "", "", err
	}
	if !validAccountReadIdentity(grant, node, user) {
		return "", "", "", ErrStaleReadingCursor
	}
	rootProof := readingHash([]interface{}{"account-discovery-root-v1", rootAccount, rootGrant.fingerprint()})
	nodeProof := readingHash([]interface{}{"account-discovery-node-v1", rootProof, path, grant.fingerprint()})
	return rootProof, nodeProof, parent, nil
}

func setDiscoveryNode(ctx context.Context, repo accountReadMembershipRepo, user string, c *accountDiscoveryCursor, node, after string) error {
	rootProof, nodeProof, _, err := discoveryProof(ctx, repo, user, c.Root, node)
	if err != nil {
		return err
	}
	if c.RootProof != "" && c.RootProof != rootProof {
		return ErrStaleReadingCursor
	}
	c.RootProof, c.NodeProof, c.Node, c.ChildAfter = rootProof, nodeProof, node, after
	return nil
}

func directDiscoveryPage(ctx context.Context, repo accountReadMembershipRepo, user, after string) (*repository.NavigationCandidatePage, error) {
	if nav, ok := repo.(accountReadNavigationRepo); ok {
		return nav.QueryNavigationCandidates(ctx, user, "accounts", "", after, 1)
	}
	// Compatibility for narrow legacy/test repositories. Production uses the
	// paginated navigation primitive above, never the user-owned collection.
	list, ok := repo.(accountReadUserListRepo)
	if !ok {
		return nil, ErrReadingData
	}
	members, err := list.ListAccountsForUser(ctx, user)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, m := range members {
		if m.UserID == user && m.AccountID > after {
			ids = append(ids, m.AccountID)
		}
	}
	sort.Strings(ids)
	p := &repository.NavigationCandidatePage{IDs: []string{}}
	if len(ids) != 0 {
		p.IDs = ids[:1]
	}
	if len(ids) > 1 {
		p.NextID = ids[0]
	}
	return p, nil
}

func nextReadableAccountCandidate(ctx context.Context, repo accountReadMembershipRepo, user string, c *accountDiscoveryCursor) ([]string, bool, error) {
	if err := c.validate(); err != nil {
		return nil, false, err
	}
	if c.Done {
		return []string{}, true, nil
	}
	if c.Root == "" {
		if c.DirectDone {
			c.Done = true
			return []string{}, true, nil
		}
		page, err := directDiscoveryPage(ctx, repo, user, c.DirectAfter)
		if err != nil {
			return nil, false, err
		}
		if page == nil || len(page.IDs) > 1 {
			return nil, false, ErrReadingData
		}
		if page.NextID != "" && (!readingID.MatchString(page.NextID) || page.NextID <= c.DirectAfter) {
			return nil, false, ErrReadingData
		}
		c.DirectDone = page.NextID == ""
		if len(page.IDs) == 0 {
			c.DirectAfter = page.NextID
			c.Done = c.DirectDone
			return []string{}, c.Done, nil
		}
		root := page.IDs[0]
		if !readingID.MatchString(root) || root <= c.DirectAfter {
			return nil, false, ErrReadingData
		}
		c.DirectAfter = root
		g, err := resolveAccountReadGrant(ctx, repo, root, user)
		if err != nil {
			return nil, false, err
		}
		if !validAccountReadIdentity(g, root, user) || g.inherited() {
			return []string{}, false, nil
		}
		c.Root = root
		if err := setDiscoveryNode(ctx, repo, user, c, root, ""); err != nil {
			return nil, false, err
		}
		return []string{root}, false, nil
	}
	rootProof, nodeProof, parent, err := discoveryProof(ctx, repo, user, c.Root, c.Node)
	if err != nil {
		return nil, false, err
	}
	if rootProof != c.RootProof || nodeProof != c.NodeProof {
		return nil, false, ErrStaleReadingCursor
	}
	children, ok := repo.(accountReadChildrenRepo)
	hierarchy, hierarchyOK := repo.(accountReadHierarchyRepo)
	if !ok || !hierarchyOK {
		c.Root, c.RootProof, c.Node, c.NodeProof, c.ChildAfter = "", "", "", "", ""
		c.Done = c.DirectDone
		return []string{}, c.Done, nil
	}
	ids, next, err := children.ListChildAccountIDsPage(ctx, c.Node, c.ChildAfter, 1)
	if err != nil {
		return nil, false, err
	}
	if len(ids) > 1 {
		return nil, false, ErrReadingData
	}
	if next != nil && (*next == "" || len(*next) > 2048 || *next == c.ChildAfter) {
		return nil, false, ErrReadingData
	}
	if len(ids) != 0 {
		child := ids[0]
		if !readingID.MatchString(child) {
			return nil, false, ErrReadingData
		}
		a, err := hierarchy.GetAccount(ctx, child)
		if err != nil {
			return nil, false, err
		}
		if canonicalReadAccount(a, child) && a.ParentAccountID == c.Node {
			g, err := resolveAccountReadGrant(ctx, repo, child, user)
			if err != nil {
				return nil, false, err
			}
			if validAccountReadIdentity(g, child, user) {
				if err := setDiscoveryNode(ctx, repo, user, c, child, ""); err != nil {
					return nil, false, err
				}
				return []string{child}, false, nil
			}
		}
	}
	if next != nil {
		c.ChildAfter = *next
		return []string{}, false, nil
	}
	if c.Node == c.Root {
		c.Root, c.RootProof, c.Node, c.NodeProof, c.ChildAfter = "", "", "", "", ""
		c.Done = c.DirectDone
		return []string{}, c.Done, nil
	}
	after, err := repository.AccountChildCursorAfter(parent, c.Node)
	if err != nil {
		return nil, false, ErrReadingData
	}
	if err := setDiscoveryNode(ctx, repo, user, c, parent, after); err != nil {
		return nil, false, err
	}
	return []string{}, false, nil
}

// queryReadableAccountCandidates keeps the flag-off navigation contract. With
// descendant reads on, each call advances one bounded provider step and embeds
// its traversal position in the outer resource-bound continuation.
func queryReadableAccountCandidates(ctx context.Context, repo accountReadMembershipRepo, user, after string, limit int, discovery **accountDiscoveryCursor) (*repository.NavigationCandidatePage, error) {
	if !descendantReadsEnabled() {
		if *discovery != nil {
			return nil, ErrStaleReadingCursor
		}
		nav, ok := repo.(accountReadNavigationRepo)
		if !ok {
			return nil, ErrReadingData
		}
		return nav.QueryNavigationCandidates(ctx, user, "accounts", "", after, limit)
	}
	if *discovery == nil {
		*discovery = &accountDiscoveryCursor{}
	}
	ids, done, err := nextReadableAccountCandidate(ctx, repo, user, *discovery)
	if err != nil {
		return nil, err
	}
	page := &repository.NavigationCandidatePage{IDs: ids}
	if !done {
		page.NextID = "discovery_pending"
	}
	return page, nil
}
