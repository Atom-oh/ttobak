package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type vaultReadingRepo struct {
	*catalogMeetingRepo
	docs                               map[string]map[string]*model.AccountDocument
	storage                            map[string]string
	transcriptReads, attachmentQueries int
	attachments                        map[string][]model.Attachment
	afterHydrate                       func()
}

func (r *vaultReadingRepo) GetAccountDocument(_ context.Context, pk, id string) (*model.AccountDocument, error) {
	doc := r.docs[pk][id]
	if doc == nil {
		return nil, nil
	}
	copy := *doc
	return &copy, nil
}
func (r *vaultReadingRepo) QueryDocumentCandidates(_ context.Context, pk, prefix, after string, limit int) (*repository.DocumentCandidatePage, error) {
	if limit != 10 || prefix != model.PrefixDoc {
		return nil, ErrInvalidInput
	}
	keys := []string{}
	for id := range r.docs[pk] {
		key := model.PrefixDoc + id
		if key > after {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	page := &repository.DocumentCandidatePage{}
	for i, key := range keys {
		if i == limit {
			page.NextKey = keys[i-1]
			break
		}
		page.Rows = append(page.Rows, repository.DocumentCandidate{SK: key})
	}
	return page, nil
}
func (r *vaultReadingRepo) ReadVaultTranscriptField(_ context.Context, id, field, value string) (string, error) {
	r.transcriptReads++
	text := value
	if strings.HasPrefix(value, "s3://") {
		var ok bool
		text, ok = r.storage[value]
		if !ok {
			return "", errors.New("source unavailable")
		}
	}
	if text == "SOURCE_TOO_LARGE" {
		return "", repository.ErrVaultSourceTooLarge
	}
	if r.afterHydrate != nil {
		fn := r.afterHydrate
		r.afterHydrate = nil
		fn()
	}
	return text, nil
}
func (r *vaultReadingRepo) QueryVaultAttachments(_ context.Context, id, after string) (*repository.VaultAttachmentPage, error) {
	r.attachmentQueries++
	rows := []model.Attachment{}
	for _, a := range r.attachments[id] {
		if a.SK > after {
			rows = append(rows, a)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].SK < rows[j].SK })
	page := &repository.VaultAttachmentPage{Rows: []model.Attachment{}}
	for i, a := range rows {
		if i == 10 {
			page.NextKey = rows[i-1].SK
			break
		}
		page.Rows = append(page.Rows, a)
	}
	return page, nil
}
func vaultReadingFixture() (*VaultReadingService, *vaultReadingRepo) {
	r := &vaultReadingRepo{catalogMeetingRepo: &catalogMeetingRepo{mockMeetingRepo: newMockMeetingRepo()}, docs: map[string]map[string]*model.AccountDocument{}, storage: map[string]string{}, attachments: map[string][]model.Attachment{}}
	r.accounts["a"] = &model.Account{AccountID: "a", Name: strings.Repeat("../고객😀\n", 100)}
	r.members["a|reader"] = &model.AccountMember{AccountID: "a", UserID: "reader", Role: "viewer"}
	for _, pk := range []string{"USER#reader", "ACCOUNT#a"} {
		r.docs[pk] = map[string]*model.AccountDocument{"d": {PK: pk, DocID: "d", Title: "../../같은 제목😀", Content: strings.Repeat("한😀\"\\\n", 30720), PublicShareToken: "PRIVATE_PUBLIC_TOKEN"}}
	}
	r.docs["ACCOUNT#a"]["d"].Content = strings.Repeat("한😀\"\\\n", 4096)
	r.docs["USER#reader"]["file"] = &model.AccountDocument{PK: "USER#reader", DocID: "file", FileKey: "docs/reader/original.pdf", Title: "File document", PublicShareToken: "PRIVATE_PUBLIC_TOKEN"}
	r.meetings["reader|m"] = &model.Meeting{UserID: "reader", MeetingID: "m", AccountID: "a", SharedToAccount: true, Title: "../../같은 제목😀", Notes: "저장된 노트", Content: "요약", TranscriptA: "s3://fixture/transcripts/m/transcriptA.txt", TranscriptB: "UNSELECTED", SelectedTranscript: "A", Date: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	r.storage[r.meetings["reader|m"].TranscriptA] = strings.Repeat("현재 선택 원문😀\"\\\n", 4000)
	for i := 0; i < 25; i++ {
		id := fmt.Sprintf("%02d", i)
		r.attachments["m"] = append(r.attachments["m"], model.Attachment{PK: "MEETING#m", SK: "ATTACH#" + id, AttachmentID: id, MeetingID: "m", FileName: "첨부 " + id, Type: "photo", Status: "done", Description: strings.Repeat("설명😀\"\\", 500)})
	}
	return NewVaultReadingService(r), r
}
func readVaultPage(t *testing.T, s *VaultReadingService, q url.Values) map[string]interface{} {
	t.Helper()
	raw, err := s.Read(context.Background(), "reader", q)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 10000 || strings.Contains(string(raw), "PRIVATE_PUBLIC_TOKEN") {
		t.Fatal("vault size/capability bound")
	}
	envelope, _ := json.Marshal(map[string]interface{}{"content": []map[string]string{{"type": "text", "text": string(raw)}}, "structuredContent": json.RawMessage(raw)})
	if len(envelope) > 32000 {
		t.Fatal("vault structured envelope bound")
	}
	var page map[string]interface{}
	if json.Unmarshal(raw, &page) != nil {
		t.Fatal("invalid JSON")
	}
	return page
}
func collectVaultManifest(t *testing.T, s *VaultReadingService) []map[string]interface{} {
	t.Helper()
	q := url.Values{"pageSize": {"8000"}}
	rows := []map[string]interface{}{}
	var buffer strings.Builder
	revision := ""
	for n := 0; ; n++ {
		p := readVaultPage(t, s, q)
		if p["discoveryPending"] != true {
			f := p["fragment"].(map[string]interface{})
			if f["startOffset"] == float64(0) {
				if buffer.Len() != 0 {
					t.Fatal("manifest group incomplete")
				}
				revision = p["revision"].(string)
			} else if revision != p["revision"] {
				t.Fatal("manifest revision changed")
			}
			buffer.WriteString(p["dataJson"].(string))
			if f["complete"] == true {
				var group []map[string]interface{}
				if json.Unmarshal([]byte(buffer.String()), &group) != nil {
					t.Fatal("manifest did not restore")
				}
				rows = append(rows, group...)
				buffer.Reset()
			}
		}
		next := p["page"].(map[string]interface{})
		if next["complete"] == true {
			break
		}
		if n > 1000 {
			t.Fatal("manifest did not finish")
		}
		q.Set("cursor", next["nextCursor"].(string))
	}
	return rows
}
func collectVaultFile(t *testing.T, s *VaultReadingService, r *vaultReadingRepo, file string) string {
	t.Helper()
	q := url.Values{"section": {"file"}, "fileId": {file}, "pageSize": {"8000"}}
	var content strings.Builder
	sourceRevision := ""
	for n := 0; ; n++ {
		before := r.attachmentQueries
		p := readVaultPage(t, s, q)
		if r.attachmentQueries-before > 2 {
			t.Fatal("unbounded attachment enumeration")
		}
		if sourceRevision == "" {
			sourceRevision = p["sourceRevision"].(string)
		} else if sourceRevision != p["sourceRevision"] {
			t.Fatal("file changed source between phases")
		}
		content.WriteString(p["markdown"].(string))
		next := p["page"].(map[string]interface{})
		if next["complete"] == true {
			break
		}
		if n > 1000 {
			t.Fatal("file did not finish")
		}
		q.Set("cursor", next["nextCursor"].(string))
	}
	return content.String()
}
func TestVaultReadingRestoresDocumentsAndMeetingAttachmentGroups(t *testing.T) {
	s, r := vaultReadingFixture()
	rows := collectVaultManifest(t, s)
	if len(rows) != 4 || r.transcriptReads != 0 || r.attachmentQueries != 0 {
		t.Fatal("manifest omitted scopes or materialized file contents")
	}
	paths := map[string]bool{}
	for _, row := range rows {
		path := row["path"].(string)
		if paths[path] || strings.HasPrefix(path, "/") || strings.Contains(path, "../") || strings.ContainsAny(path, "\x00\n\\") {
			t.Fatal("unsafe/colliding path")
		}
		paths[path] = true
		for _, segment := range strings.Split(path, "/") {
			if len(segment) > 255 {
				t.Fatal("oversize filename component")
			}
		}
		token, err := decodeVaultFile(row["fileId"].(string), "reader")
		if err != nil {
			t.Fatal(err)
		}
		if row["mode"] == "non_text" {
			page := readVaultPage(t, s, url.Values{"section": {"file"}, "fileId": {row["fileId"].(string)}})
			if page["markdown"] != nil || page["documentRead"].(map[string]interface{})["docId"] != "file" {
				t.Fatal("file document pretended text or lost download reference")
			}
			continue
		}
		got := collectVaultFile(t, s, r, row["fileId"].(string))
		var expected string
		if token.Kind == "document" {
			pk := "USER#reader"
			if token.Account != "" {
				pk = "ACCOUNT#" + token.Account
			}
			doc := r.docs[pk][token.ID]
			expected = buildDocFrontmatter(doc) + doc.Content
		} else {
			m := *r.meetings["reader|m"]
			m.TranscriptA = r.storage[m.TranscriptA]
			expected = buildFrontmatter(&m, r.accounts["a"].Name) + GenerateMeetingDocument(&m, r.attachments["m"])
		}
		if got != expected {
			t.Fatalf("%s export did not restore exact original markdown: %d/%d", path, len(got), len(expected))
		}
	}
}
func TestVaultReadingManifestTraversesBeyondLegacyMeetingCap(t *testing.T) {
	s, r := vaultReadingFixture()
	r.meetings = map[string]*model.Meeting{}
	r.docs = map[string]map[string]*model.AccountDocument{}
	r.members = map[string]*model.AccountMember{}
	for i := 0; i < 351; i++ {
		id := fmt.Sprintf("m-%03d", i)
		r.meetings["reader|"+id] = &model.Meeting{MeetingID: id, UserID: "reader", Title: "same"}
	}
	rows := collectVaultManifest(t, s)
	if len(rows) != 351 || r.transcriptReads != 0 {
		t.Fatal("legacy 300-row truncation survived")
	}
}
func TestVaultReadingPinsCurrentSourceGrantAndActualSpillBytes(t *testing.T) {
	for _, change := range []string{"body", "member", "privacy-marker", "spill", "attachment"} {
		t.Run(change, func(t *testing.T) {
			s, r := vaultReadingFixture()
			kind, account, id := "document", "a", "d"
			if change == "spill" || change == "attachment" {
				kind, account, id = "meeting", "", "m"
			}
			source, err := s.source(context.Background(), "reader", kind, account, id)
			if err != nil {
				t.Fatal(err)
			}
			q := url.Values{"section": {"file"}, "fileId": {encodeVaultFile(source.token)}, "pageSize": {"10"}}
			p := readVaultPage(t, s, q)
			if change == "attachment" {
				q.Set("pageSize", "8000")
				for p["phase"] != "attachments" {
					q.Set("cursor", p["page"].(map[string]interface{})["nextCursor"].(string))
					p = readVaultPage(t, s, q)
				}
			}
			q.Set("cursor", p["page"].(map[string]interface{})["nextCursor"].(string))
			switch change {
			case "body":
				r.docs["ACCOUNT#a"]["d"].Content = "changed without timestamp"
			case "member":
				delete(r.members, "a|reader")
			case "privacy-marker":
				r.members["a|reader"].SummaryPrivacyVersion = 1
			case "spill":
				r.storage[r.meetings["reader|m"].TranscriptA] = "changed same S3 key"
			case "attachment":
				r.attachments["m"][0].Description = "changed"
			}
			raw, err := s.Read(context.Background(), "reader", q)
			if raw != nil || !(errors.Is(err, ErrStaleReadingCursor) || errors.Is(err, ErrForbidden)) {
				t.Fatal("stale file returned", err)
			}
		})
	}
}

func TestVaultReadingHydratedSelectionRetainsFallbackAndItsErrors(t *testing.T) {
	for _, selected := range []string{"A", "B"} {
		for _, fallback := range []string{"FALLBACK_TRANSCRIPT", "SOURCE_TOO_LARGE", "missing"} {
			t.Run(selected+"/"+fallback, func(t *testing.T) {
				s, r := vaultReadingFixture()
				m := r.meetings["reader|m"]
				m.TranscriptA = "s3://fixture/transcripts/m/transcriptA.txt"
				m.TranscriptB = "s3://fixture/transcripts/m/transcriptB.txt"
				m.SelectedTranscript, m.TranscriptSegments = selected, ""
				first, alternate := m.TranscriptA, m.TranscriptB
				if selected == "B" {
					first, alternate = alternate, first
				}
				r.storage[first] = " \n\t"
				delete(r.storage, alternate)
				if fallback != "missing" {
					r.storage[alternate] = fallback
				}
				source, err := s.source(context.Background(), "reader", "meeting", "", "m")
				if err != nil {
					t.Fatal(err)
				}
				text, err := s.body(context.Background(), source)
				if fallback == "FALLBACK_TRANSCRIPT" {
					if err != nil || !strings.Contains(text, fallback) {
						t.Fatal("hydrated blank selection lost valid alternate", err)
					}
				} else if err == nil || text != "" || (fallback == "SOURCE_TOO_LARGE" && !errors.Is(err, repository.ErrVaultSourceTooLarge)) {
					t.Fatal("fallback failure was hidden", err)
				}
				if r.transcriptReads != 2 {
					t.Fatal("unexpected transcript hydration count", r.transcriptReads)
				}
			})
		}
	}
	s, r := vaultReadingFixture()
	r.meetings["reader|m"].TranscriptB = "s3://fixture/transcripts/m/transcriptB.txt"
	source, _ := s.source(context.Background(), "reader", "meeting", "", "m")
	if _, err := s.body(context.Background(), source); err != nil || r.transcriptReads != 1 {
		t.Fatal("valid selected text fetched an unavailable alternate", err)
	}
}
func TestVaultReadingRejectsForeignIdentityAndRechecksDuringHydration(t *testing.T) {
	s, r := vaultReadingFixture()
	source, _ := s.source(context.Background(), "reader", "meeting", "", "m")
	file := encodeVaultFile(source.token)
	if _, err := s.Read(context.Background(), "other", url.Values{"section": {"file"}, "fileId": {file}}); !errors.Is(err, ErrForbidden) {
		t.Fatal("foreign file identity granted access", err)
	}
	r.afterHydrate = func() { delete(r.members, "a|reader") }
	if raw, err := s.Read(context.Background(), "reader", url.Values{"section": {"file"}, "fileId": {file}}); raw != nil || !errors.Is(err, ErrStaleReadingCursor) {
		t.Fatal("account-name grant revoked during hydration was accepted", err)
	}
	fresh, _ := s.source(context.Background(), "reader", "meeting", "", "m")
	if strings.HasPrefix(fresh.path, "Accounts/") || fresh.accountName != "" {
		t.Fatal("classification leaked inaccessible account name")
	}
	r.storage[r.meetings["reader|m"].TranscriptA] = "SOURCE_TOO_LARGE"
	if raw, err := s.Read(context.Background(), "reader", url.Values{"section": {"file"}, "fileId": {encodeVaultFile(fresh.token)}}); raw != nil || !errors.Is(err, repository.ErrVaultSourceTooLarge) {
		t.Fatal("oversize source was clipped or replaced")
	}
}

func TestVaultReadingAttachmentTrailingEmptyProviderPagePreservesFinalNewline(t *testing.T) {
	rows := []model.Attachment{{AttachmentID: "a", FileName: "a", Type: "photo", Status: "done"}}
	got := renderVaultAttachmentGroup(rows, true, false) + renderVaultAttachmentGroup(nil, false, true)
	expected := renderVaultAttachmentGroup(rows, true, true)
	if got != expected {
		t.Fatal("provider end probe dropped the Markdown final newline")
	}
}
