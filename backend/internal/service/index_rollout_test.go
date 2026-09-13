package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

type bootstrapRepository struct{ IndexRepository }

func (r bootstrapRepository) ScanIndexSources(context.Context, string, int32) ([]model.IndexResource, string, error) {
	return nil, "", errors.New("manual bootstrap scanned canonical sources")
}
func (r bootstrapRepository) GetIndexSource(context.Context, model.IndexResource) (*model.IndexRecord, error) {
	return nil, errors.New("manual bootstrap read a canonical source")
}

type bootstrapObjects struct{ IndexObjects }

func (p bootstrapObjects) LegacyPage(context.Context, string) ([]string, string, error) {
	return nil, "", errors.New("manual bootstrap enumerated legacy meeting exports")
}

func TestManualOnlyBootstrapPreservesMeetingExportsThenEnablesCanonical(t *testing.T) {
	s, repo, store, provider, _ := newKnowledgeTest()
	meeting := addIndexMeeting(repo, "meeting", "current saved notes")
	private := store.original("kb/owner/existing.pdf", "private current bytes", "v1")
	shared := store.original("shared/reference/existing.docx", "shared current bytes", "v1")
	legacy := "meetings/owner/meeting.md"
	store.kb[legacy] = []byte("legacy meeting recall")
	canonical := meeting.Prefix() + "existing/meeting.md"
	store.kb[canonical] = []byte("existing canonical export")
	before := indexClone(repo.sources)
	// A queued canonical event from an earlier deployment must stay untouched.
	if err := s.Enqueue(context.Background(), meeting); err != nil {
		t.Fatal(err)
	}
	queued := indexClone(repo.jobs[meeting.Hash()])
	if err := s.SetIndexingMode(IndexModeManualOnly); err != nil {
		t.Fatal(err)
	}
	s.repo, s.objects = bootstrapRepository{repo}, bootstrapObjects{store}
	if err := s.Enqueue(context.Background(), meeting); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err := s.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		finishIndex(t, s, provider)
		if repo.jobs[private.Hash()] != nil && repo.jobs[shared.Hash()] != nil &&
			repo.jobs[private.Hash()].State == model.IndexIndexed && repo.jobs[shared.Hash()].State == model.IndexIndexed {
			break
		}
	}
	for _, resource := range []model.IndexResource{private, shared} {
		if job := repo.jobs[resource.Hash()]; job == nil || job.State != model.IndexIndexed {
			t.Fatalf("automatic manual-only bootstrap did not index %s", resource.Kind)
		}
	}
	if !reflect.DeepEqual(before, repo.sources) || !reflect.DeepEqual(queued, repo.jobs[meeting.Hash()]) {
		t.Fatal("manual-only bootstrap touched a canonical source/job")
	}
	if string(store.kb[legacy]) != "legacy meeting recall" || string(store.kb[canonical]) != "existing canonical export" {
		t.Fatal("manual-only bootstrap broke existing meeting recall")
	}
	for _, event := range store.events {
		if strings.Contains(event, meeting.Prefix()) || event == "delete:"+legacy {
			t.Fatalf("canonical S3 mutation during manual bootstrap: %s", event)
		}
	}
	// This switch represents the later deployment after QA is current-source aware.
	s.repo, s.objects = repo, store
	if err := s.SetIndexingMode(IndexModeAll); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10 && repo.jobs[meeting.Hash()].State != model.IndexIndexed; i++ {
		if _, err := s.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		finishIndex(t, s, provider)
	}
	if repo.jobs[meeting.Hash()].State != model.IndexIndexed {
		t.Fatal("canonical sources did not recover after enabling all")
	}
	if _, exists := store.kb[legacy]; exists {
		t.Fatal("canonical activation failed to retire the old meeting export")
	}
}

func TestManualOnlyRejectsUnsafeDowngradeAndCanonicalInFlight(t *testing.T) {
	for _, phase := range []string{"IDLE", "EXPORTING", "PREPARED", "RUNNING", "FINALIZING", "legacy"} {
		t.Run(phase, func(t *testing.T) {
			s, repo, store, provider, _ := newKnowledgeTest()
			meeting := addIndexMeeting(repo, "meeting", "notes")
			if _, err := s.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			repo.control.Phase = phase
			if phase == "legacy" {
				repo.control.Phase, repo.control.Mode = "RUNNING", ""
			}
			if phase == "IDLE" {
				repo.control.Batch = nil
			}
			if err := s.SetIndexingMode(IndexModeManualOnly); err != nil {
				t.Fatal(err)
			}
			before, objects, starts := indexClone(repo.control), indexClone(store.kb), len(provider.tokens)
			if _, err := s.Tick(context.Background()); !errors.Is(err, ErrIndexMode) {
				t.Fatalf("unsafe downgrade/canonical batch was accepted: %v", err)
			}
			if !reflect.DeepEqual(before, repo.control) || !reflect.DeepEqual(objects, store.kb) || len(provider.tokens) != starts {
				t.Fatal("rejected rollout mode changed data or the coordinator")
			}
			if repo.jobs[meeting.Hash()] == nil {
				t.Fatal("fixture did not create a canonical batch")
			}
		})
	}
	for _, mode := range []string{"", "manual", "ALL", "manual-only ", "unknown"} {
		s, _, _, _, _ := newKnowledgeTest()
		if err := s.SetIndexingMode(mode); !errors.Is(err, ErrIndexMode) {
			t.Fatalf("invalid mode accepted: %q", mode)
		}
	}
}
