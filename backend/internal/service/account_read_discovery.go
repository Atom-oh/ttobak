package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

const maxReadableAccounts = 1000

// accountNavigationPageSize is the repository's navigation page maximum.
const accountNavigationPageSize = 50

// Account list pages stop after this many discovery steps or this much
// traversal time, whichever comes first. Each step performs at most one
// index/child query; one page still always advances by at least one step.
const (
	accountListDiscoveryStepBudget = 256
	accountListDiscoveryTimeBudget = 2 * time.Second
	accountRecheckConcurrency      = 8
)

// discoveryReads returns the request-scoped traversal reader for one page.
func (s *AccountService) discoveryReads() interface {
	accountReadMembershipRepo
	accountReadHierarchyRepo
} {
	if cached, ok := newDiscoveryReadCache(s.repo).(*discoveryReadCache); ok {
		return cached
	}
	return s.repo
}

// recheckAll runs independent fresh rechecks with bounded concurrency and
// returns the error of the earliest failing receipt, matching sequential order.
func recheckAll(ctx context.Context, concurrency int, receipts [][2]string, check func(id, revision string) error) error {
	if concurrency < 1 {
		concurrency = 1
	}
	errs := make([]error, len(receipts))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, receipt := range receipts {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, id, revision string) {
			defer wg.Done()
			defer func() { <-sem }()
			errs[i] = check(id, revision)
		}(i, receipt[0], receipt[1])
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return ctx.Err()
}

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
	candidateIDs := append([]string{}, hints...)
	if nav, ok := repo.(accountReadNavigationRepo); ok {
		// Each page holds up to accountNavigationPageSize ascending IDs. The
		// collection still fails at 10,000 index candidates, as the former
		// one-ID-per-page walk did at its 10,000-page bound.
		after := ""
		indexed := 0
		for pages := 0; ; pages++ {
			if pages >= 10000 {
				return nil, ErrReadingData
			}
			page, err := nav.QueryNavigationCandidates(ctx, userID, "accounts", "", after, accountNavigationPageSize)
			if err != nil {
				return nil, err
			}
			if page == nil || len(page.IDs) > accountNavigationPageSize {
				return nil, ErrReadingData
			}
			previous := after
			for _, id := range page.IDs {
				if !readingID.MatchString(id) || id <= previous {
					return nil, ErrReadingData
				}
				previous = id
				candidateIDs = append(candidateIDs, id)
			}
			if indexed += len(page.IDs); indexed >= 10000 {
				return nil, ErrReadingData
			}
			if page.NextID == "" {
				break
			}
			if !readingID.MatchString(page.NextID) || page.NextID <= after || page.NextID < previous {
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
				candidateIDs = append(candidateIDs, m.AccountID)
			}
		}
	}
	// Discovery reads go through a request-scoped view primed by batch reads;
	// every grant it yields is revalidated below against a fresh view. The
	// capability checks keep using repo itself.
	var reads accountReadMembershipRepo = repo
	if view := batchedAccountReads(repo); view != nil {
		if err := view.prime(ctx, userID, distinctCandidateIDs(candidateIDs), true); err != nil {
			return nil, err
		}
		reads = view
	}
	candidates := prefetchReadableCandidates(ctx, reads, userID, candidateIDs)
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
		g, a, err := candidates.read(ctx, id)
		if err != nil {
			return err
		}
		if g == nil {
			return nil
		}
		if _, ok := repo.(accountReadHierarchyRepo); ok {
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
	for _, id := range candidateIDs {
		if err := add(id); err != nil {
			return nil, err
		}
	}
	children, canDiscover := repo.(accountReadChildrenRepo)
	hierarchy, canValidate := repo.(accountReadHierarchyRepo)
	if descendantReadsEnabled() && canDiscover && canValidate {
		childReads := hierarchy
		if view, ok := reads.(*discoveryReadCache); ok {
			childReads = view
		}
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
				if err := primeAccountReads(ctx, reads, userID, distinctCandidateIDs(ids), true); err != nil {
					return nil, err
				}
				for _, id := range ids {
					if !readingID.MatchString(id) {
						return nil, ErrReadingData
					}
					a, err := childReads.GetAccount(ctx, id)
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
	recheck := repo
	if view := batchedAccountReads(repo); view != nil {
		ids := make([]string, 0, len(grants))
		for id, grant := range grants {
			if grant != nil {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		if err := view.prime(ctx, userID, ids, true); err != nil {
			return nil, err
		}
		recheck = view
	}
	if err := revalidateAccountReadGrants(ctx, recheck, grants, userID, ErrStaleReadingCursor); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(grants))
	for id := range grants {
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
	scope := "all"
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
		case "scope":
			if values[0] != "all" && values[0] != "direct" {
				return nil, ErrInvalidReadingOptions
			}
			scope = values[0]
		default:
			return nil, ErrInvalidReadingOptions
		}
	}
	if scope == "direct" {
		return s.listDirectAccountsPage(ctx, user, size, rawCursor)
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
	// Rechecks read current rows through a fresh view primed with one batch
	// read per phase, in place of the per-receipt GetMember/GetAccount reads.
	var fresh accountReads = s.repo
	recheckAccount := func(id, revision string) error {
		if id == "" {
			if revision != "" {
				return ErrInvalidReadingCursor
			}
			return nil
		}
		g, err := resolveAccountReadGrant(ctx, fresh, id, user)
		if err != nil {
			return err
		}
		a, err := fresh.GetAccount(ctx, id)
		if err != nil {
			return err
		}
		if !validAccountReadIdentity(g, id, user) || a == nil || a.AccountID != id ||
			readingHash([]interface{}{a, g.fingerprint()}) != revision {
			return ErrStaleReadingCursor
		}
		return nil
	}
	freshView := func(receipts [][2]string) error {
		ids := make([]string, 0, len(receipts))
		for _, receipt := range receipts {
			if receipt[0] != "" {
				ids = append(ids, receipt[0])
			}
		}
		view, err := primedAccountReads(ctx, s.repo, user, ids, true)
		fresh = view
		return err
	}
	if err := freshView([][2]string{{c.Anchor, c.Revision}}); err != nil {
		return nil, err
	}
	if err := recheckAccount(c.Anchor, c.Revision); err != nil {
		return nil, err
	}
	page := &AccountListPage{Accounts: []model.AccountSummary{}, OverlapPossible: true, UnionKey: "accountId"}
	observed := [][2]string{}
	done := false
	// Traversal reads go through a request-scoped cache; every returned row and
	// the cursor anchor are rechecked below against the uncached repository.
	reads := s.discoveryReads()
	started := time.Now()
	// The step and time budgets are fixed, so per-page work stays independent of
	// total tree width while one page covers a typical account tree.
	for steps := 0; steps < accountListDiscoveryStepBudget && len(page.Accounts) < size && !done; steps++ {
		if steps > 0 && time.Since(started) >= accountListDiscoveryTimeBudget {
			break
		}
		ids, complete, err := nextReadableAccountCandidate(ctx, reads, user, c.Discovery)
		if err != nil {
			return nil, err
		}
		done = complete
		for _, id := range ids {
			g, err := requireReadGrantVia(ctx, reads, user, id)
			if err != nil {
				return nil, err
			}
			a, err := reads.GetAccount(ctx, id)
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
	// The anchor is the last returned row unless this page returned none.
	if anchor := [2]string{c.Anchor, c.Revision}; len(observed) == 0 || observed[len(observed)-1] != anchor {
		observed = append(observed, anchor)
	}
	if err := freshView(observed); err != nil {
		return nil, err
	}
	if err := recheckAll(ctx, s.recheckConcurrency, observed, recheckAccount); err != nil {
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
func discoveryProof(ctx context.Context, repo accountReadMembershipRepo, user, root, node string) (string, string, []repository.AccountParentLink, error) {
	hierarchy, ok := repo.(accountReadHierarchyRepo)
	if !ok {
		return "", "", nil, ErrReadingData
	}
	rootGrant, err := resolveAccountReadGrant(ctx, repo, root, user)
	if err != nil {
		return "", "", nil, err
	}
	if !validAccountReadIdentity(rootGrant, root, user) || rootGrant.inherited() {
		return "", "", nil, ErrStaleReadingCursor
	}
	path := []repository.AccountParentLink{}
	seen := map[string]bool{}
	var rootAccount *model.Account
	for id := node; ; {
		if !readingID.MatchString(id) || seen[id] || len(path) > repository.AccountHierarchyMaxAncestors {
			return "", "", nil, ErrStaleReadingCursor
		}
		seen[id] = true
		a, err := hierarchy.GetAccount(ctx, id)
		if err != nil {
			return "", "", nil, err
		}
		// Legacy direct records retain their existing identity contract. A
		// descendant traversal always requires canonical records throughout.
		if a == nil || a.AccountID != id || (node != root && !canonicalReadAccount(a, id)) {
			return "", "", nil, ErrStaleReadingCursor
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
		return "", "", nil, err
	}
	if !validAccountReadIdentity(grant, node, user) {
		return "", "", nil, ErrStaleReadingCursor
	}
	rootProof := readingHash([]interface{}{"account-discovery-root-v1", rootAccount, rootGrant.fingerprint()})
	nodeProof := readingHash([]interface{}{"account-discovery-node-v1", rootProof, path, grant.fingerprint()})
	return rootProof, nodeProof, path, nil
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
		// One batch read primes the root's META and membership rows.
		if err := primeAccountReads(ctx, repo, user, []string{root}, true); err != nil {
			return nil, false, err
		}
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
	rootProof, nodeProof, path, err := discoveryProof(ctx, repo, user, c.Root, c.Node)
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
		if err := primeAccountReads(ctx, repo, user, []string{child}, true); err != nil {
			return nil, false, err
		}
		a, err := hierarchy.GetAccount(ctx, child)
		if err != nil {
			return nil, false, err
		}
		// A direct grant remains valid independently of malformed ancestry,
		// but traversal must not revisit its current path or exceed the bound.
		canDescend := len(path) <= repository.AccountHierarchyMaxAncestors
		for _, link := range path {
			if link.AccountID == child {
				canDescend = false
				break
			}
		}
		if canDescend && canonicalReadAccount(a, child) && a.ParentAccountID == c.Node {
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
	after, err := repository.AccountChildCursorAfter(path[0].ParentAccountID, c.Node)
	if err != nil {
		return nil, false, ErrReadingData
	}
	if err := setDiscoveryNode(ctx, repo, user, c, path[0].ParentAccountID, after); err != nil {
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
