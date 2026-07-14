package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type vaultRepo interface {
	ListMeetings(ctx context.Context, params repository.ListMeetingsParams) (*repository.ListMeetingsResult, error)
	GetMeeting(ctx context.Context, userID, meetingID string) (*model.Meeting, error)
	ListAttachments(ctx context.Context, meetingID string) ([]model.Attachment, error)
	GetAccount(ctx context.Context, accountID string) (*model.Account, error)
	ListAccountsForUser(ctx context.Context, userID string) ([]model.AccountMember, error)
	ListAccountDocuments(ctx context.Context, pk string) ([]model.AccountDocument, error)
	GetAccountDocument(ctx context.Context, pk, docID string) (*model.AccountDocument, error)
}

// VaultRepo is the exported alias for cross-package tests.
type VaultRepo = vaultRepo

type VaultService struct {
	repo vaultRepo
}

func NewVaultService(repo *repository.DynamoDBRepository) *VaultService { return &VaultService{repo: repo} }
func newVaultServiceWithRepo(repo vaultRepo) *VaultService            { return &VaultService{repo: repo} }
func NewVaultServiceForTest(repo VaultRepo) *VaultService             { return &VaultService{repo: repo} }

var fnameReplacer = strings.NewReplacer("/", "-", "\\", "-", ":", "-", "*", "-", "?", "", "\"", "'", "<", "(", ">", ")", "|", "-", "\n", " ")

func sanitizeFilename(s string) string {
	out := strings.TrimSpace(fnameReplacer.Replace(s))
	// Path-traversal guard: the result becomes a single path segment in the
	// exported vault (e.g. Accounts/{name}/...). The replacer already strips
	// "/" and "\", so the only escape left is a ".." segment — neutralize it so
	// an account named ".." can't climb out of its folder.
	out = strings.ReplaceAll(out, "..", "_")
	return out
}

// insightCountsLine returns a deterministic `risk: 1, opportunity: 2` style string.
func insightCountsLine(insightsJSON string) string {
	if strings.TrimSpace(insightsJSON) == "" {
		return ""
	}
	var items []model.MeetingInsight
	if json.Unmarshal([]byte(insightsJSON), &items) != nil || len(items) == 0 {
		return ""
	}
	counts := map[string]int{}
	for _, it := range items {
		counts[it.Type]++
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s: %d", k, counts[k]))
	}
	return strings.Join(parts, ", ")
}

func buildFrontmatter(meeting *model.Meeting, accountName string) string {
	var b strings.Builder
	b.WriteString("---\n")
	if accountName != "" {
		b.WriteString(fmt.Sprintf("account: \"[[%s]]\"\n", accountName))
	}
	b.WriteString(fmt.Sprintf("date: %s\n", meeting.Date.Format("2006-01-02")))
	if len(meeting.Participants) > 0 {
		b.WriteString(fmt.Sprintf("participants: [%s]\n", strings.Join(meeting.Participants, ", ")))
	}
	tags := append([]string{"meeting"}, meeting.Tags...)
	b.WriteString(fmt.Sprintf("tags: [%s]\n", strings.Join(tags, ", ")))
	b.WriteString(fmt.Sprintf("status: %s\n", meeting.Status))
	if line := insightCountsLine(meeting.Insights); line != "" {
		b.WriteString(fmt.Sprintf("insights: {%s}\n", line))
	}
	b.WriteString(fmt.Sprintf("ttobak_id: %s\n", meeting.MeetingID))
	b.WriteString("---\n\n")
	return b.String()
}

func vaultPath(meeting *model.Meeting, accountName string) string {
	title := sanitizeFilename(meeting.Title)
	if title == "" {
		title = meeting.MeetingID
	}
	fname := fmt.Sprintf("%s %s.md", meeting.Date.Format("2006-01-02"), title)
	if meeting.SharedToAccount && accountName != "" {
		return fmt.Sprintf("Accounts/%s/%s", sanitizeFilename(accountName), fname)
	}
	return "_Private/Meetings/" + fname
}

// buildDocFrontmatter mirrors buildFrontmatter for a note/blog document.
// ttobak_id closes the ADR-017 loop guard: re-ingesting this exported file
// via PutDocument/UpdateDocument is rejected as TTOBAK-origin.
func buildDocFrontmatter(doc *model.AccountDocument) string {
	var b strings.Builder
	b.WriteString("---\n")
	if doc.DocType != "" {
		b.WriteString(fmt.Sprintf("doc_type: %s\n", doc.DocType))
	}
	if len(doc.Links) > 0 {
		links := make([]string, len(doc.Links))
		for i, l := range doc.Links {
			links[i] = fmt.Sprintf("\"[[%s]]\"", l)
		}
		b.WriteString(fmt.Sprintf("links: [%s]\n", strings.Join(links, ", ")))
	}
	b.WriteString(fmt.Sprintf("ttobak_id: %s\n", doc.DocID))
	b.WriteString("---\n\n")
	return b.String()
}

// docVaultPath places a doc under Accounts/{name}/Docs/ (shared) or
// _Private/Docs/ (personal). seen tracks paths already emitted this export
// so two same-titled docs don't collide.
func docVaultPath(doc *model.AccountDocument, accountName string, seen map[string]bool) string {
	title := sanitizeFilename(doc.Title)
	if title == "" {
		title = doc.DocID
	}
	dir := "_Private/Docs"
	if accountName != "" {
		dir = fmt.Sprintf("Accounts/%s/Docs", sanitizeFilename(accountName))
	}
	path := fmt.Sprintf("%s/%s.md", dir, title)
	if seen[path] {
		path = fmt.Sprintf("%s/%s %s.md", dir, title, doc.DocID[:8])
	}
	seen[path] = true
	return path
}

// docScope is one PK to sweep for documents: personal (accountName == "") or
// one account membership.
type docScope struct {
	pk          string
	accountName string
}

// exportDocuments appends every note/blog document (personal + each account
// membership) to files. Slides (FileName set, no markdown Content) are
// skipped -- the vault export is markdown-only.
func (s *VaultService) exportDocuments(ctx context.Context, userID string, nameCache map[string]string) ([]model.VaultFile, error) {
	files := []model.VaultFile{}
	scopes := []docScope{{pk: model.PrefixUser + userID}}
	memberships, err := s.repo.ListAccountsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, m := range memberships {
		name, ok := nameCache[m.AccountID]
		if !ok {
			acc, err := s.repo.GetAccount(ctx, m.AccountID)
			if err != nil {
				// Skip this account's documents rather than mis-filing them
				// under _Private/Docs/ -- accountName == "" reads as
				// "personal" to docVaultPath, which would misclassify a
				// shared account's documents as private on a transient
				// read error.
				continue
			}
			if acc != nil {
				name = acc.Name
			}
			nameCache[m.AccountID] = name
		}
		scopes = append(scopes, docScope{pk: model.PrefixAccount + m.AccountID, accountName: name})
	}

	seenPaths := map[string]bool{}
	for _, scope := range scopes {
		stubs, err := s.repo.ListAccountDocuments(ctx, scope.pk)
		if err != nil {
			return nil, err
		}
		for _, stub := range stubs {
			if stub.FileName != "" {
				continue // slide -- no markdown content to export
			}
			full, err := s.repo.GetAccountDocument(ctx, scope.pk, stub.DocID)
			if err != nil {
				return nil, err
			}
			if full == nil || full.Content == "" {
				continue
			}
			md := buildDocFrontmatter(full) + full.Content
			files = append(files, model.VaultFile{Path: docVaultPath(full, scope.accountName, seenPaths), Markdown: md})
		}
	}
	return files, nil
}

// ExportVault renders the user's owned meetings as Obsidian-ready markdown files,
// placed under Accounts/{name}/ (if shared to an account) or _Private/Meetings/.
// Account names are resolved once and cached. (Account MOC files are a follow-up;
// the `account: "[[name]]"` frontmatter already drives Obsidian's graph.)
// maxVaultMeetings caps how many meetings one export materializes in memory.
// Each meeting pulls its full transcript + summary, so an unbounded export over a
// large corpus could OOM/timeout the Lambda. Declared as a var so tests can lower it.
var maxVaultMeetings = 300

func (s *VaultService) ExportVault(ctx context.Context, userID string) ([]model.VaultFile, error) {
	files := []model.VaultFile{}
	nameCache := map[string]string{}
	cursor := ""
	processed := 0
	truncated := false
exportLoop:
	for {
		res, err := s.repo.ListMeetings(ctx, repository.ListMeetingsParams{UserID: userID, Tab: "all", Cursor: cursor, Limit: 100})
		if err != nil {
			return nil, err
		}
		for i := range res.Meetings {
			if processed >= maxVaultMeetings {
				truncated = true
				break exportLoop
			}
			full, err := s.repo.GetMeeting(ctx, userID, res.Meetings[i].MeetingID)
			if err != nil {
				return nil, err
			}
			if full == nil {
				continue
			}
			accountName := ""
			if full.SharedToAccount && full.AccountID != "" {
				name, ok := nameCache[full.AccountID]
				if !ok {
					if acc, err := s.repo.GetAccount(ctx, full.AccountID); err == nil && acc != nil {
						name = acc.Name
					}
					nameCache[full.AccountID] = name
				}
				accountName = name
			}
			attachments, _ := s.repo.ListAttachments(ctx, full.MeetingID)
			md := buildFrontmatter(full, accountName) + GenerateMeetingDocument(full, attachments)
			files = append(files, model.VaultFile{Path: vaultPath(full, accountName), Markdown: md})
			processed++
		}
		if res.NextCursor == nil {
			break
		}
		cursor = *res.NextCursor
	}
	// Surface truncation rather than silently capping (no silent caps).
	if truncated {
		files = append(files, model.VaultFile{
			Path:     "_export-truncated.md",
			Markdown: fmt.Sprintf("# Export truncated\n\nCapped at %d meetings to bound memory. Older meetings were omitted — narrow the range or export per-account to retrieve them.\n", maxVaultMeetings),
		})
	}

	docFiles, err := s.exportDocuments(ctx, userID, nameCache)
	if err != nil {
		return nil, err
	}
	files = append(files, docFiles...)

	return files, nil
}
