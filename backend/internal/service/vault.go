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

func sanitizeFilename(s string) string { return strings.TrimSpace(fnameReplacer.Replace(s)) }

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
	return files, nil
}
