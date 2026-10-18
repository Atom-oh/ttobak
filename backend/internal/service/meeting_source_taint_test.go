package service

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

// sourceWriterAllowlist classifies every function in internal/service and
// internal/handler that writes a term source field (design A2.1). Actor
// writers must call markNonOwnerSourceEdit; the others have no user actor.
var sourceWriterAllowlist = map[string]string{
	"service/meeting.go:UpdateMeeting":                   "actor",
	"service/meeting.go:UpdateSpeakers":                  "actor",
	"service/meeting.go:SelectTranscript":                "actor",
	"service/meeting.go:UpdateMeetingTranscript":         "internal: no caller, no actor",
	"service/meeting.go:UpdateMeetingContent":            "internal: no caller, no actor",
	"service/transcribe.go:ProcessTranscriptionComplete": "machine transcript publisher",
	"service/account.go:updateDoc":                       "account document, not a meeting",
	"service/document_reading.go:Read":                   "reading options, not a write",
	"handler/meeting.go:CreateMeeting":                   "response body, not a write",
}

// TestSourceWriterAllowlist fails when a new function writes content,
// transcriptA/B, selectedTranscript, notes or liveSummary (or calls a notes
// compare-and-set writer) without being classified here, and
// when an actor path stops marking non-owner edits.
func TestSourceWriterAllowlist(t *testing.T) {
	sourceKeys := map[string]bool{`"content"`: true, `"transcriptA"`: true, `"transcriptB"`: true, `"selectedTranscript"`: true,
		`"notes"`: true, `"liveSummary"`: true}
	notesWriters := map[string]bool{"UpdateMeetingNotesWithRevision": true, "UpdateMeetingNotesIfMatch": true}
	found := map[string]bool{}
	marks := map[string]bool{}
	fset := token.NewFileSet()
	for dir, pkg := range map[string]string{".": "service", "../handler": "handler"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil || fn.Name.Name == "markNonOwnerSourceEdit" {
					continue
				}
				name := pkg + "/" + filepath.Base(path) + ":" + fn.Name.Name
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					switch x := n.(type) {
					case *ast.CallExpr:
						if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "markNonOwnerSourceEdit" {
							marks[name] = true
						}
						if sel, ok := x.Fun.(*ast.SelectorExpr); ok && notesWriters[sel.Sel.Name] {
							found[name] = true
						}
					case *ast.AssignStmt:
						for _, lhs := range x.Lhs {
							if ix, ok := lhs.(*ast.IndexExpr); ok {
								if lit, ok := ix.Index.(*ast.BasicLit); ok && sourceKeys[lit.Value] {
									found[name] = true
								}
							}
						}
					case *ast.CompositeLit:
						if mt, ok := x.Type.(*ast.MapType); ok {
							if _, iface := mt.Value.(*ast.InterfaceType); iface {
								for _, elt := range x.Elts {
									if kv, ok := elt.(*ast.KeyValueExpr); ok {
										if lit, ok := kv.Key.(*ast.BasicLit); ok && sourceKeys[lit.Value] {
											found[name] = true
										}
									}
								}
							}
						}
					}
					return true
				})
			}
		}
	}
	var names []string
	for name := range found {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		kind, ok := sourceWriterAllowlist[name]
		if !ok {
			t.Errorf("%s writes a term source field but is not in sourceWriterAllowlist", name)
			continue
		}
		if kind == "actor" && !marks[name] {
			t.Errorf("%s is an actor writer but does not call markNonOwnerSourceEdit", name)
		}
	}
	for name, kind := range sourceWriterAllowlist {
		if kind == "actor" && !found[name] {
			t.Errorf("allowlisted actor writer %s no longer writes a source field; update the allowlist", name)
		}
	}
}

func TestNonOwnerSourceEditsSetStickyTaint(t *testing.T) {
	cases := []struct {
		name    string
		actor   string
		run     func(*MeetingService, string) error
		tainted bool
	}{
		{"editor content", "editor-1", func(s *MeetingService, a string) error {
			_, err := s.UpdateMeeting(context.Background(), a, "m-1", &model.UpdateMeetingRequest{Content: "planted notes"})
			return err
		}, true},
		{"editor transcriptA", "editor-1", func(s *MeetingService, a string) error {
			_, err := s.UpdateMeeting(context.Background(), a, "m-1", &model.UpdateMeetingRequest{TranscriptA: "planted transcript"})
			return err
		}, true},
		{"editor selected transcript via update", "editor-1", func(s *MeetingService, a string) error {
			_, err := s.UpdateMeeting(context.Background(), a, "m-1", &model.UpdateMeetingRequest{SelectedTranscript: "B"})
			return err
		}, true},
		{"editor select transcript", "editor-1", func(s *MeetingService, a string) error {
			return s.SelectTranscript(context.Background(), a, "m-1", "B")
		}, true},
		{"editor speaker rename", "editor-1", func(s *MeetingService, a string) error {
			_, err := s.UpdateSpeakers(context.Background(), a, "m-1", &model.UpdateSpeakersRequest{SpeakerMap: map[string]string{"spk_0": "Kim"}})
			return err
		}, true},
		{"editor saved notes", "editor-1", func(s *MeetingService, a string) error {
			notes := "Vendor: ZetaCorpX"
			_, err := s.UpdateMeeting(context.Background(), a, "m-1", &model.UpdateMeetingRequest{Notes: &notes})
			return err
		}, true},
		{"editor saved notes compare-and-set", "editor-1", func(s *MeetingService, a string) error {
			notes, expected := "Vendor: ZetaCorpX", "owner memo"
			_, err := s.UpdateMeeting(context.Background(), a, "m-1", &model.UpdateMeetingRequest{Notes: &notes, ExpectedNotes: &expected})
			return err
		}, true},
		{"editor unchanged notes compare-and-set", "editor-1", func(s *MeetingService, a string) error {
			notes := "owner memo"
			_, err := s.UpdateMeeting(context.Background(), a, "m-1", &model.UpdateMeetingRequest{Notes: &notes, ExpectedNotes: &notes})
			return err
		}, false},
		{"owner saved notes compare-and-set", "owner-1", func(s *MeetingService, a string) error {
			notes, expected := "owner update", "owner memo"
			_, err := s.UpdateMeeting(context.Background(), a, "m-1", &model.UpdateMeetingRequest{Notes: &notes, ExpectedNotes: &expected})
			return err
		}, false},
		{"editor live summary", "editor-1", func(s *MeetingService, a string) error {
			live := "ZetaCorpX was agreed"
			_, err := s.UpdateMeeting(context.Background(), a, "m-1", &model.UpdateMeetingRequest{LiveSummary: &live})
			return err
		}, true},
		{"editor unchanged content", "editor-1", func(s *MeetingService, a string) error {
			_, err := s.UpdateMeeting(context.Background(), a, "m-1", &model.UpdateMeetingRequest{Content: "original notes"})
			return err
		}, false},
		{"editor title only", "editor-1", func(s *MeetingService, a string) error {
			_, err := s.UpdateMeeting(context.Background(), a, "m-1", &model.UpdateMeetingRequest{Title: "New"})
			return err
		}, false},
		{"owner content", "owner-1", func(s *MeetingService, a string) error {
			_, err := s.UpdateMeeting(context.Background(), a, "m-1", &model.UpdateMeetingRequest{Content: "owner notes"})
			return err
		}, false},
		{"owner select transcript", "owner-1", func(s *MeetingService, a string) error {
			return s.SelectTranscript(context.Background(), a, "m-1", "B")
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newMockMeetingRepo()
			now := time.Now().UTC()
			repo.addMeeting(&model.Meeting{
				MeetingID: "m-1", UserID: "owner-1", Status: model.StatusDone, Content: "original notes", Notes: "owner memo",
				TranscriptA: "spk_0: hello", TranscriptB: "spk_0: hello b", Date: now, CreatedAt: now, UpdatedAt: now,
			})
			repo.shares[shareKey("editor-1", "m-1")] = &model.Share{MeetingID: "m-1", OwnerID: "owner-1", SharedToID: "editor-1", Permission: model.PermissionEdit}
			if err := tc.run(newMeetingServiceWithRepo(repo), tc.actor); err != nil {
				t.Fatal(err)
			}
			got := repo.meetingsByID["m-1"].SourceEditedByNonOwnerAt != ""
			if got != tc.tainted {
				t.Fatalf("tainted = %v, want %v", got, tc.tainted)
			}
		})
	}
}

type sweepingMeetingRepo struct {
	*mockMeetingRepo
	swept []string
	err   error
}

func (r *sweepingMeetingRepo) SweepMeetingTerms(_ context.Context, ownerID, meetingID string) error {
	r.swept = append(r.swept, ownerID+"/"+meetingID)
	return r.err
}

// A failed post-delete term sweep leaves the meeting gone; the owner's retried
// DELETE re-runs the sweep and surfaces its failure instead of a bare 404.
func TestDeleteMeetingRetryReRunsTermSweep(t *testing.T) {
	cases := []struct {
		name      string
		existing  bool
		sweepErr  error
		wantErr   error
		wantSwept int
	}{
		{"gone meeting is swept again", false, nil, ErrNotFound, 1},
		{"sweep failure surfaces", false, errors.New("throttled"), nil, 1},
		{"another owner's meeting is not swept", true, nil, ErrForbidden, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := newMockMeetingRepo()
			if tc.existing {
				now := time.Now().UTC()
				base.addMeeting(&model.Meeting{MeetingID: "m-1", UserID: "owner-2", Status: model.StatusDone, Date: now, CreatedAt: now, UpdatedAt: now})
			}
			repo := &sweepingMeetingRepo{mockMeetingRepo: base, err: tc.sweepErr}
			err := newMeetingServiceWithRepo(repo).DeleteMeeting(context.Background(), "owner-1", "m-1")
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) || tc.wantErr == nil && !errors.Is(err, tc.sweepErr) {
				t.Fatalf("err = %v", err)
			}
			if len(repo.swept) != tc.wantSwept || tc.wantSwept == 1 && repo.swept[0] != "owner-1/m-1" {
				t.Fatalf("swept = %v", repo.swept)
			}
		})
	}
}
