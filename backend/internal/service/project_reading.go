package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type ProjectReadingRepository interface {
	projectRepo
	QueryProjectReading(context.Context, string, string, string) (*repository.ProjectReadingPage, error)
	GetProjectReadingResearch(context.Context, string) (*model.Research, error)
}
type ProjectReadingService struct{ repo ProjectReadingRepository }

func NewProjectReadingService(repo ProjectReadingRepository) *ProjectReadingService {
	return &ProjectReadingService{repo}
}

type projectReadingCursor struct {
	sectionReadingCursor
	GrantKind          string `json:"g"`
	Account            string `json:"a"`
	Grant              string `json:"h"`
	Scan               int    `json:"s"`
	InheritedCandidate string `json:"i,omitempty"`
}

func projectReadingPhases(section string) []string {
	if section == "brief" {
		return []string{"metadata", "members", "meetings", "research", "insights"}
	}
	if section == "research" {
		return []string{"research"}
	}
	return accountReadingPhases(section)
}
func encodeProjectReadingCursor(c projectReadingCursor) (string, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	if len(encoded) > 2048 {
		return "", ErrReadingData
	}
	return encoded, nil
}

// The selected grant is checked again, without falling through to another path.
// A changed grant must restart a read, even if a different grant still exists.
func (s *ProjectReadingService) grant(ctx context.Context, user string, p *model.Project, c projectReadingCursor) (string, error) {
	switch c.GrantKind {
	case "owner":
		if p.OwnerUserID == user {
			return readingHash([]string{"owner", user}), nil
		}
	case "direct":
		m, err := s.repo.GetProjectMember(ctx, p.ProjectID, user)
		if err != nil {
			return "", err
		}
		if m != nil && m.ProjectID == p.ProjectID && m.UserID == user {
			return readingHash(m), nil
		}
	case "account":
		if !contains(p.AccountIDs, c.Account) {
			return "", ErrForbidden
		}
		grant, err := resolveAccountReadGrant(ctx, s.repo, c.Account, user)
		if err != nil {
			return "", err
		}
		if grant != nil && grant.Member.UserID == user {
			return grant.fingerprint(), nil
		}
	default:
		return "", ErrInvalidReadingCursor
	}
	return "", ErrForbidden
}

func (s *ProjectReadingService) authorize(ctx context.Context, user string, p *model.Project, c *projectReadingCursor) (bool, error) {
	if c.GrantKind != "" {
		if c.InheritedCandidate != "" {
			return false, ErrInvalidReadingCursor
		}
		grant, err := s.grant(ctx, user, p, *c)
		if err != nil {
			return false, err
		}
		if c.Grant != grant {
			return false, ErrStaleReadingCursor
		}
		return true, nil
	}
	if c.Grant != "" || c.Account != "" || c.Scan < 0 || c.Scan > len(p.AccountIDs) || c.Offset != 0 || c.After != "" || c.Revision != "" {
		return false, ErrInvalidReadingCursor
	}
	if c.InheritedCandidate != "" && !contains(p.AccountIDs[:c.Scan], c.InheritedCandidate) {
		return false, ErrInvalidReadingCursor
	}
	if p.OwnerUserID == user {
		c.GrantKind = "owner"
	} else {
		member, err := s.repo.GetProjectMember(ctx, p.ProjectID, user)
		if err != nil {
			return false, err
		}
		if member != nil && member.ProjectID == p.ProjectID && member.UserID == user {
			c.GrantKind = "direct"
		}
	}
	if c.GrantKind == "" {
		end := c.Scan + 50
		if end > len(p.AccountIDs) {
			end = len(p.AccountIDs)
		}
		var lookupErr error
		for _, id := range p.AccountIDs[c.Scan:end] {
			grant, err := resolveAccountReadGrant(ctx, s.repo, id, user)
			if err != nil {
				lookupErr = err
				continue
			}
			if validAccountReadIdentity(grant, id, user) {
				if grant.inherited() {
					if c.InheritedCandidate == "" {
						c.InheritedCandidate = id
					}
					continue
				}
				c.GrantKind = "account"
				c.Account = id
				break
			}
		}
		if c.GrantKind == "" {
			if lookupErr != nil {
				return false, lookupErr
			}
			c.Scan = end
			if end < len(p.AccountIDs) {
				return false, nil
			}
			if c.InheritedCandidate == "" {
				return false, ErrForbidden
			}
			c.GrantKind = "account"
			c.Account = c.InheritedCandidate
		}
	}
	grant, err := s.grant(ctx, user, p, *c)
	if err != nil {
		return false, err
	}
	c.Grant = grant
	c.Scan = 0
	c.InheritedCandidate = ""
	return true, nil
}

// The public service still applies canonical link/trashed/Evidence rules, with
// a ten-row collection view and consistent, owner-bound primary source reads.
// Its project access helper sees only the already-validated account path.
type projectReadingView struct {
	projectRepo
	source  ProjectReadingRepository
	project *model.Project
	cursor  projectReadingCursor
	page    *repository.ProjectReadingPage
}

func (v *projectReadingView) GetShare(ctx context.Context, user, id string) (*model.Share, error) {
	if shares, ok := v.projectRepo.(interface {
		GetShare(context.Context, string, string) (*model.Share, error)
	}); ok {
		return shares.GetShare(ctx, user, id)
	}
	return nil, nil
}

func (v *projectReadingView) GetProject(_ context.Context, id string) (*model.Project, error) {
	if id != v.project.ProjectID {
		return nil, ErrForbidden
	}
	p := *v.project
	p.AccountIDs = nil
	if v.cursor.GrantKind == "account" {
		p.AccountIDs = []string{v.cursor.Account}
	}
	return &p, nil
}
func (v *projectReadingView) ListProjectMembers(_ context.Context, id string) ([]model.ProjectMember, error) {
	if id != v.project.ProjectID {
		return nil, ErrForbidden
	}
	return v.page.Members, nil
}
func (v *projectReadingView) ListProjectMeetingRefsForProject(_ context.Context, id string) ([]model.ProjectMeetingRef, error) {
	if id != v.project.ProjectID {
		return nil, ErrForbidden
	}
	return v.page.Meetings, nil
}
func (v *projectReadingView) ListProjectResearchRefsForProject(_ context.Context, id string) ([]model.ProjectResearchRef, error) {
	if id != v.project.ProjectID {
		return nil, ErrForbidden
	}
	return v.page.Research, nil
}
func (v *projectReadingView) BatchGetMeetings(ctx context.Context, keys []repository.MeetingKey) ([]*model.Meeting, error) {
	if len(keys) > 10 {
		return nil, ErrReadingData
	}
	out := []*model.Meeting{}
	for _, key := range keys {
		m, err := v.source.GetMeeting(ctx, key.OwnerID, key.MeetingID)
		if err != nil {
			return nil, err
		}
		if m != nil {
			if m.UserID != key.OwnerID || m.MeetingID != key.MeetingID {
				return nil, ErrReadingData
			}
			out = append(out, m)
		}
	}
	return out, nil
}
func (v *projectReadingView) BatchGetResearchByIDs(ctx context.Context, ids []string) ([]model.Research, error) {
	if len(ids) > 10 {
		return nil, ErrReadingData
	}
	out := []model.Research{}
	for _, id := range ids {
		item, err := v.source.GetProjectReadingResearch(ctx, id)
		if err != nil {
			return nil, err
		}
		if item == nil {
			continue
		}
		if item.ResearchID != id {
			return nil, ErrReadingData
		}
		for _, ref := range v.page.Research {
			if ref.ResearchID == id && ref.OwnerUserID == item.UserID {
				out = append(out, *item)
				break
			}
		}
	}
	return out, nil
}
func (s *ProjectReadingService) payload(ctx context.Context, user string, p *model.Project, c projectReadingCursor, after string, o sectionReadingOptions) (string, string, error) {
	page := &repository.ProjectReadingPage{}
	var err error
	if c.Phase != "metadata" {
		page, err = s.repo.QueryProjectReading(ctx, p.ProjectID, c.Phase, after)
		if err != nil {
			return "", "", err
		}
		if page == nil || len(page.Members)+len(page.Meetings)+len(page.Research) > 10 {
			return "", "", ErrReadingData
		}
	}
	view := &projectReadingView{projectRepo: s.repo, source: s.repo, project: p, cursor: c, page: page}
	access := &ProjectService{repo: view}
	var value interface{}
	switch c.Phase {
	case "metadata":
		response := projectResponse(p, nil)
		if c.GrantKind == "account" {
			grant, err := resolveAccountReadGrant(ctx, s.repo, c.Account, user)
			if err != nil {
				return "", "", err
			}
			if !validAccountReadIdentity(grant, c.Account, user) {
				return "", "", ErrForbidden
			}
			if grant.inherited() {
				response.AccessPermission = model.PermissionRead
				response.InheritedFromAccountID = grant.InheritedFromAccountID
			}
		}
		raw, _ := json.Marshal(response)
		var metadata map[string]interface{}
		if json.Unmarshal(raw, &metadata) != nil {
			return "", "", ErrReadingData
		}
		delete(metadata, "members")
		value = metadata
	case "members":
		value = projectResponse(p, page.Members).Members
	case "meetings":
		value, err = access.ListProjectMeetings(ctx, user, p.ProjectID)
	case "research":
		value, err = access.ListProjectResearch(ctx, user, p.ProjectID)
	case "insights":
		value, err = access.GetProjectInsights(ctx, user, p.ProjectID, o.from, o.to, o.types)
	default:
		return "", "", ErrInvalidReadingCursor
	}
	if err != nil {
		return "", "", err
	}
	raw, err := json.Marshal(value)
	return string(raw), page.NextKey, err
}
func (s *ProjectReadingService) project(ctx context.Context, id string) (*model.Project, error) {
	p, err := s.repo.GetProject(ctx, id)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, ErrNotFound
	}
	if p.ProjectID != id {
		return nil, ErrReadingData
	}
	copy := *p
	copy.AccountIDs = append([]string{}, p.AccountIDs...)
	sort.Strings(copy.AccountIDs)
	return &copy, nil
}
func (s *ProjectReadingService) Read(ctx context.Context, user, id string, q url.Values) ([]byte, error) {
	if user == "" {
		return nil, ErrForbidden
	}
	if !readingID.MatchString(id) {
		return nil, ErrInvalidReadingOptions
	}
	o, err := parseSectionReadingFor(q, projectReadingPhases)
	if err != nil {
		return nil, err
	}
	p, err := s.project(ctx, id)
	if err != nil {
		return nil, err
	}
	projectRevision := readingHash(p)
	binding := compactReadingHash([]interface{}{"project-reading-v1", user, id, o.section, o.from, o.to, o.types, projectRevision})
	phases := projectReadingPhases(o.section)
	c := projectReadingCursor{sectionReadingCursor: sectionReadingCursor{Version: 1, Binding: binding, Phase: phases[0]}}
	if o.cursor != "" {
		raw, err := base64.RawURLEncoding.Strict().DecodeString(o.cursor)
		if err != nil || json.Unmarshal(raw, &c) != nil || c.Version != 1 || c.Offset < 0 {
			return nil, ErrInvalidReadingCursor
		}
		canonical, _ := json.Marshal(c)
		if !bytes.Equal(raw, canonical) || (c.Offset > 0 && c.Revision == "") {
			return nil, ErrInvalidReadingCursor
		}
		if c.Binding != binding {
			return nil, ErrStaleReadingCursor
		}
	}
	phaseIndex := -1
	for i, phase := range phases {
		if phase == c.Phase {
			phaseIndex = i
		}
	}
	if phaseIndex < 0 {
		return nil, ErrInvalidReadingCursor
	}
	authorized, err := s.authorize(ctx, user, p, &c)
	if err != nil {
		return nil, err
	}
	if !authorized {
		latest, err := s.project(ctx, id)
		if err != nil {
			return nil, err
		}
		if readingHash(latest) != projectRevision {
			return nil, ErrStaleReadingCursor
		}
		cursor, err := encodeProjectReadingCursor(c)
		if err != nil {
			return nil, err
		}
		return json.Marshal(readingObject{"kind": "project", "projectId": id, "section": o.section, "phase": "authorization",
			"authorizationPending": true, "page": readingObject{"complete": false, "nextCursor": cursor}})
	}
	after, err := base64.RawURLEncoding.Strict().DecodeString(c.After)
	if err != nil || !utf8.Valid(after) || len(after) > 1024 || (c.Phase == "metadata" && len(after) != 0) ||
		(c.Phase != "metadata" && len(after) != 0 && !strings.HasPrefix(string(after), repository.ProjectReadingPrefix(c.Phase))) {
		return nil, ErrInvalidReadingCursor
	}
	text, nextKey, err := s.payload(ctx, user, p, c, string(after), o)
	if err != nil {
		return nil, err
	}
	group := compactReadingHash([]interface{}{c.Phase, string(after), nextKey, text, c.Grant})
	if c.Revision != "" && c.Revision != group {
		return nil, ErrStaleReadingCursor
	}
	innerBinding := readingHash([]string{binding, group})
	inner := ReadingOptions{PageSize: o.pageSize, MaxBytes: 10000}
	if c.Offset > 0 {
		raw, _ := json.Marshal(readingCursor{1, innerBinding, c.Offset})
		inner.Cursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	revisionBytes, _ := base64.RawURLEncoding.DecodeString(group)
	var renderError error
	body, err := paginateReading(text, []readingWindow{{end: utf8.RuneCountInString(text)}}, inner, innerBinding, func(chunks []readingChunk, fragment readingObject) readingObject {
		renderError = nil
		part := ""
		for _, chunk := range chunks {
			part += chunk.Text
		}
		next := c
		done := fragment["complete"].(bool)
		complete := done && nextKey == "" && phaseIndex == len(phases)-1
		var cursor interface{}
		if !complete {
			if !done {
				next.Offset, next.Revision = fragment["endOffset"].(int), group
			} else if nextKey != "" {
				next.After, next.Offset, next.Revision = base64.RawURLEncoding.EncodeToString([]byte(nextKey)), 0, ""
			} else {
				next.Phase, next.After, next.Offset, next.Revision = phases[phaseIndex+1], "", 0, ""
			}
			cursor, renderError = encodeProjectReadingCursor(next)
		}
		delete(fragment, "nextCursor")
		return readingObject{"kind": "project", "projectId": id, "section": o.section, "phase": c.Phase, "consistency": "current_group",
			"overlapPossible": c.Phase == "meetings" || c.Phase == "insights", "revision": hex.EncodeToString(revisionBytes), "dataJson": part,
			"fragment": fragment, "page": readingObject{"complete": complete, "nextCursor": cursor}}
	})
	if err != nil {
		return nil, err
	}
	if renderError != nil {
		return nil, renderError
	}
	latest, err := s.project(ctx, id)
	if err != nil {
		return nil, err
	}
	if readingHash(latest) != projectRevision {
		return nil, ErrStaleReadingCursor
	}
	grant, err := s.grant(ctx, user, latest, c)
	if err != nil {
		return nil, err
	}
	if grant != c.Grant {
		return nil, ErrStaleReadingCursor
	}
	fresh, freshNext, err := s.payload(ctx, user, latest, c, string(after), o)
	if err != nil {
		if errors.Is(err, ErrForbidden) || errors.Is(err, ErrNotFound) {
			return nil, ErrStaleReadingCursor
		}
		return nil, err
	}
	if fresh != text || freshNext != nextKey {
		return nil, ErrStaleReadingCursor
	}
	// Source rereads can perform I/O; check the selected grant and project again afterwards.
	latest, err = s.project(ctx, id)
	if err != nil {
		return nil, err
	}
	if readingHash(latest) != projectRevision {
		return nil, ErrStaleReadingCursor
	}
	grant, err = s.grant(ctx, user, latest, c)
	if err != nil {
		return nil, err
	}
	if grant != c.Grant {
		return nil, ErrStaleReadingCursor
	}
	return body, nil
}
