package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

const testBucket = "kb-bucket"

type fakeStore struct{ research map[string]*model.Research }

func (f *fakeStore) GetTermsSourceRun(context.Context, string, string, string) (*model.TermsSourceRun, error) {
	return nil, nil
}
func (f *fakeStore) PutTermsSourceRun(context.Context, *model.TermsSourceRun) error { return nil }
func (f *fakeStore) GetResearchByID(_ context.Context, id string) (*model.Research, error) {
	return f.research[id], nil
}

type fakeObjects struct {
	meta  map[string]map[string]string
	heads int
}

func (f *fakeObjects) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	f.heads++
	meta, ok := f.meta[aws.ToString(in.Key)]
	if !ok {
		return nil, errors.New("unexpected head")
	}
	return &s3.HeadObjectOutput{Metadata: meta}, nil
}
func (f *fakeObjects) GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	return nil, errors.New("backfill never reads object bodies")
}

// pagedLister serves keys per prefix two at a time.
func pagedLister(keys map[string][]string) keyLister {
	return func(_ context.Context, prefix, cursor string) ([]string, string, error) {
		all := keys[prefix]
		start := 0
		if cursor != "" {
			for i, k := range all {
				if k == cursor {
					start = i
				}
			}
		}
		end := min(start+2, len(all))
		next := ""
		if end < len(all) {
			next = all[end]
		}
		return all[start:end], next, nil
	}
}

func TestBackfillMapsSourcesThroughProductionMapper(t *testing.T) {
	keys := map[string][]string{
		"kb/": {
			"kb/user-1/1700000000_notes.md",  // KB text with an Account
			"kb/user-2/1700000001_guide.txt", // KB text, personal
			"kb/user-1/1700000002_deck.pptx", // unsupported type: no head
			"kb/user-1/x.md.metadata.json",   // sidecar: unsupported
			"kb/not/an/owner/key.md",         // malformed: skipped
		},
		"shared/research/": {
			"shared/research/r-1.md",           // report of a known research
			"shared/research/r-1.json",         // not a report
			"shared/research/r-1/section-a.md", // section file
			"shared/research/r-missing.md",     // unknown research
		},
	}
	objects := &fakeObjects{meta: map[string]map[string]string{
		"kb/user-1/1700000000_notes.md":  {"account-id": "acct-1"},
		"kb/user-2/1700000001_guide.txt": {},
	}}
	store := &fakeStore{research: map[string]*model.Research{
		"r-1": {UserID: "user-3", AccountIDs: []string{"acct-2"}},
	}}

	var published []model.TermsSourceRequested
	counted, queued := countingPublisher(func(_ context.Context, ev model.TermsSourceRequested) error {
		published = append(published, ev)
		return nil
	})
	cfg := service.TermsConfig{KBMode: service.SourceTermsModeOn, ResearchMode: service.SourceTermsModeOn}
	terms := service.NewTermsAnalysisService(nil, nil, nil, nil, nil, nil, cfg)
	terms.SetSourceReader(service.TermsSourceDeps{Store: store, Objects: objects, Bucket: testBucket, Publish: counted})

	var slept []time.Duration
	opts := backfillOptions{bucket: testBucket, interval: time.Second, queued: queued,
		sleep: func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }}
	result, err := backfill(context.Background(), pagedLister(keys), terms.RequestSourceFromObject, opts)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if want := (backfillResult{Scanned: 9, Queued: 3, Failed: 0}); result != want {
		t.Fatalf("result = %+v, want %+v", result, want)
	}
	want := []model.TermsSourceRequested{
		{Kind: model.DictSourceKB, Ref: "kb/user-1/1700000000_notes.md", OwnerID: "user-1", AccountIDs: []string{"acct-1"}},
		{Kind: model.DictSourceKB, Ref: "kb/user-2/1700000001_guide.txt", OwnerID: "user-2"},
		{Kind: model.DictSourceResearch, Ref: "r-1", OwnerID: "user-3", AccountIDs: []string{"acct-2"}},
	}
	if !reflect.DeepEqual(published, want) {
		t.Fatalf("published = %+v, want %+v", published, want)
	}
	if objects.heads != 2 {
		t.Fatalf("heads = %d, want 2 (only supported KB text)", objects.heads)
	}
	if len(slept) != 3 {
		t.Fatalf("slept %d times, want once per queued request", len(slept))
	}
}

func TestBackfillOptions(t *testing.T) {
	keys := map[string][]string{"kb/": {"kb/a/1", "kb/a/2", "kb/a/3"}, "shared/research/": {"shared/research/r.md"}}
	errMap := errors.New("map failed")
	tests := []struct {
		name     string
		limit    int
		interval time.Duration
		failKey  string
		want     backfillResult
		sleeps   int
	}{
		{name: "all keys, no spacing", want: backfillResult{Scanned: 4, Queued: 4}},
		{name: "limit stops the scan", limit: 2, interval: time.Second, want: backfillResult{Scanned: 2, Queued: 2}, sleeps: 2},
		{name: "mapper error is counted and the run continues", failKey: "kb/a/2", interval: time.Second,
			want: backfillResult{Scanned: 4, Queued: 3, Failed: 1}, sleeps: 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			n := 0
			mapper := func(_ context.Context, bucket, key string) error {
				if bucket != testBucket {
					t.Fatalf("bucket = %q", bucket)
				}
				if key == tc.failKey {
					return errMap
				}
				n++
				return nil
			}
			sleeps := 0
			opts := backfillOptions{bucket: testBucket, limit: tc.limit, interval: tc.interval, queued: func() int { return n },
				sleep: func(context.Context, time.Duration) error { sleeps++; return nil }}
			got, err := backfill(context.Background(), pagedLister(keys), mapper, opts)
			if err != nil {
				t.Fatalf("backfill: %v", err)
			}
			if got != tc.want || sleeps != tc.sleeps {
				t.Fatalf("got %+v sleeps=%d, want %+v sleeps=%d", got, sleeps, tc.want, tc.sleeps)
			}
		})
	}
}

func TestBackfillStopsOnListError(t *testing.T) {
	errList := errors.New("list failed")
	list := func(context.Context, string, string) ([]string, string, error) { return nil, "", errList }
	_, err := backfill(context.Background(), list, func(context.Context, string, string) error { return nil },
		backfillOptions{bucket: testBucket, queued: func() int { return 0 }})
	if !errors.Is(err, errList) {
		t.Fatalf("err = %v, want list error", err)
	}
}

func TestDryRunPublisherNeverSends(t *testing.T) {
	counted, queued := countingPublisher(nil)
	if err := counted(context.Background(), model.TermsSourceRequested{Kind: model.DictSourceKB}); err != nil {
		t.Fatalf("dry-run publish: %v", err)
	}
	if queued() != 1 {
		t.Fatalf("queued = %d, want 1", queued())
	}
}
