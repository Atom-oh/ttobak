package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type VaultReadingRepository interface {
	GetMeeting(context.Context, string, string) (*model.Meeting, error)
	GetAccountDocument(context.Context, string, string) (*model.AccountDocument, error)
	GetMember(context.Context, string, string) (*model.AccountMember, error)
	GetAccount(context.Context, string) (*model.Account, error)
	QueryMeetingCatalog(context.Context, string, string, string, string, int) (*repository.MeetingCatalogPage, error)
	QueryDocumentCandidates(context.Context, string, string, string, int) (*repository.DocumentCandidatePage, error)
	QueryNavigationCandidates(context.Context, string, string, string, string, int) (*repository.NavigationCandidatePage, error)
	QueryVaultAttachments(context.Context, string, string) (*repository.VaultAttachmentPage, error)
	ReadVaultTranscriptField(context.Context, string, string, string) (string, error)
}
type VaultReadingService struct{ repo VaultReadingRepository }

func NewVaultReadingService(repo *repository.DynamoDBRepository) *VaultReadingService {
	// Metadata and authorization reads must never eagerly hydrate spill fields.
	// body applies the Vault-specific bound only to the selected source.
	return &VaultReadingService{repo: repo.MetadataView()}
}

type vaultFileIdentity struct {
	Version  int    `json:"v"`
	User     string `json:"u"`
	Kind     string `json:"k"`
	ID       string `json:"i"`
	Account  string `json:"a"`
	Revision string `json:"r"`
}
type vaultReadingSource struct {
	token             vaultFileIdentity
	meeting           *model.Meeting
	doc               *model.AccountDocument
	accountName, path string
}

func vaultSafeName(value string, maxBytes int) string {
	value = sanitizeFilename(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '-'
		}
		return r
	}, value))
	for len(value) > maxBytes {
		_, size := utf8.DecodeLastRuneInString(value)
		value = value[:len(value)-size]
	}
	value = strings.Trim(value, " .")
	if value == "" {
		return "Untitled"
	}
	return value
}
func encodeVaultFile(token vaultFileIdentity) string {
	raw, _ := json.Marshal(token)
	return base64.RawURLEncoding.EncodeToString(raw)
}
func decodeVaultFile(raw, user string) (vaultFileIdentity, error) {
	var token vaultFileIdentity
	data, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil || len(raw) > 2048 || json.Unmarshal(data, &token) != nil || token.Version != 1 || !readingID.MatchString(token.ID) || (token.Kind != "meeting" && token.Kind != "document") || (token.Account != "" && !readingID.MatchString(token.Account)) || (token.Kind == "meeting" && token.Account != "") {
		return token, ErrInvalidReadingOptions
	}
	canonical, _ := json.Marshal(token)
	if !bytes.Equal(data, canonical) {
		return token, ErrInvalidReadingOptions
	}
	if token.User != compactReadingHash(user) {
		return token, ErrForbidden
	}
	return token, nil
}
func (s *VaultReadingService) source(ctx context.Context, user, kind, account, id string) (*vaultReadingSource, error) {
	source := &vaultReadingSource{token: vaultFileIdentity{Version: 1, User: compactReadingHash(user), Kind: kind, ID: id, Account: account}}
	grant := compactReadingHash(user)
	var accountData *model.Account
	accountPath := ""
	accountID := account
	if kind == "meeting" {
		m, err := s.repo.GetMeeting(ctx, user, id)
		if err != nil {
			return nil, err
		}
		if m == nil {
			return nil, ErrNotFound
		}
		if m.UserID != user || m.MeetingID != id {
			return nil, ErrReadingData
		}
		copy := *m
		source.meeting = &copy
		if m.SharedToAccount {
			accountID = m.AccountID
		}
	} else if kind == "document" {
		pk := model.PrefixUser + user
		if account != "" {
			pk = model.PrefixAccount + account
		}
		// Account grant is checked before fetching any document bytes.
		if account != "" {
			m, err := s.repo.GetMember(ctx, account, user)
			if err != nil {
				return nil, err
			}
			if m == nil || m.AccountID != account || m.UserID != user {
				return nil, ErrForbidden
			}
			grant = accountReadingGrant(m)
		}
		doc, err := s.repo.GetAccountDocument(ctx, pk, id)
		if err != nil {
			return nil, err
		}
		if doc == nil {
			return nil, ErrNotFound
		}
		if doc.PK != pk || doc.DocID != id {
			return nil, ErrReadingData
		}
		copy := *doc
		copy.PublicShareToken = ""
		source.doc = &copy
	} else {
		return nil, ErrInvalidReadingOptions
	}
	if accountID != "" {
		member, err := s.repo.GetMember(ctx, accountID, user)
		if err != nil {
			return nil, err
		}
		if member != nil && member.AccountID == accountID && member.UserID == user {
			accountData, err = s.repo.GetAccount(ctx, accountID)
			if err != nil {
				return nil, err
			}
			if accountData != nil {
				if accountData.AccountID != accountID {
					return nil, ErrReadingData
				}
				source.accountName = accountData.Name
				accountPath = "Accounts/" + vaultSafeName(accountData.Name, 60) + "--" + accountID
				grant = accountReadingGrant(member)
			}
		}
		if kind == "document" && accountData == nil {
			return nil, ErrForbidden
		}
	}
	if kind == "meeting" {
		dir := "_Private/Meetings"
		if accountPath != "" {
			dir = accountPath + "/Meetings"
		}
		source.path = dir + "/" + source.meeting.Date.Format("2006-01-02") + " " + vaultSafeName(source.meeting.Title, 70) + "--" + id + ".md"
	} else {
		dir := "_Private/Docs"
		if accountPath != "" {
			dir = accountPath + "/Docs"
		}
		source.path = dir + "/" + vaultSafeName(source.doc.Title, 80) + "--" + id + ".md"
	}
	source.token.Revision = readingHash([]interface{}{source.meeting, source.doc, accountData, grant, source.path})
	return source, nil
}
func (s *VaultReadingService) body(ctx context.Context, source *vaultReadingSource) (string, error) {
	if source.doc != nil {
		if source.doc.FileKey != "" {
			return "", ErrInvalidReadingOptions
		}
		return buildDocFrontmatter(source.doc) + source.doc.Content, nil
	}
	m := *source.meeting
	_, variant := selectMeetingTranscript(&m)
	field, value := "transcriptA", m.TranscriptA
	if variant == "B" {
		field, value = "transcriptB", m.TranscriptB
	}
	text, err := s.repo.ReadVaultTranscriptField(ctx, m.MeetingID, field, value)
	if err != nil {
		return "", err
	}
	// Metadata contains a nonempty spill reference even when its current bytes
	// are blank. Match the normal hydrated A/B fallback without fetching an
	// unused variant when the selected source has text.
	if strings.TrimSpace(text) == "" {
		fallbackField, fallbackValue, fallbackVariant := "transcriptB", m.TranscriptB, "B"
		if variant == "B" {
			fallbackField, fallbackValue, fallbackVariant = "transcriptA", m.TranscriptA, "A"
		}
		if strings.TrimSpace(fallbackValue) != "" {
			text, err = s.repo.ReadVaultTranscriptField(ctx, m.MeetingID, fallbackField, fallbackValue)
			if err != nil {
				return "", err
			}
			variant = fallbackVariant
		}
	}
	segments := ""
	if strings.TrimSpace(text) != "" && m.TranscriptSegments != "" {
		segments, err = s.repo.ReadVaultTranscriptField(ctx, m.MeetingID, "transcriptSegments", m.TranscriptSegments)
		if err != nil {
			return "", err
		}
	}
	m.TranscriptA, m.TranscriptB, m.TranscriptSegments = "", "", segments
	if variant == "B" {
		m.TranscriptB = text
	} else {
		m.TranscriptA = text
	}
	return buildFrontmatter(&m, source.accountName) + GenerateMeetingDocument(&m, nil), nil
}
func renderVaultAttachmentGroup(rows []model.Attachment, first, last bool) string {
	var b strings.Builder
	if first && len(rows) > 0 {
		b.WriteString("## Attachments\n\n")
	}
	for _, att := range rows {
		name := att.FileName
		if name == "" {
			name = att.AttachmentID
		}
		fmt.Fprintf(&b, "- **%s** (type: %s, status: %s)\n", name, att.Type, att.Status)
		if att.Description != "" {
			fmt.Fprintf(&b, "  - Description: %s\n", att.Description)
		}
	}
	if last && (len(rows) > 0 || !first) {
		b.WriteString("\n")
	}
	return b.String()
}
