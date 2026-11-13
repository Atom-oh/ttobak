package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

type fanoutMock struct {
	pages        map[string][]model.UpdateFeedSub // cursor -> page
	nextCursor   map[string]string
	listErr      error
	states       map[string]*model.UpdateFeedState
	entries      map[string][]model.UpdateFeedEntry
	stateCalls   map[string]int
	entryCalls   map[string]int
	lastSince    map[string]time.Time
	putErr       map[string]error // by userID
	pendingURLs  []string
	marked       []string
	markErr      error
	fannedAt     map[string]time.Time // userID -> at
	existingURLs map[string]bool
}

func newFanoutMock() *fanoutMock {
	return &fanoutMock{
		pages: map[string][]model.UpdateFeedSub{}, nextCursor: map[string]string{},
		states: map[string]*model.UpdateFeedState{}, entries: map[string][]model.UpdateFeedEntry{},
		stateCalls: map[string]int{}, entryCalls: map[string]int{}, lastSince: map[string]time.Time{},
		putErr: map[string]error{}, fannedAt: map[string]time.Time{},
		existingURLs: map[string]bool{},
	}
}

func (m *fanoutMock) ListFeedSubs(_ context.Context, cursor string, _ int) ([]model.UpdateFeedSub, string, error) {
	if m.listErr != nil {
		return nil, "", m.listErr
	}
	return m.pages[cursor], m.nextCursor[cursor], nil
}
func (m *fanoutMock) GetFeedState(_ context.Context, h string) (*model.UpdateFeedState, error) {
	m.stateCalls[h]++
	return m.states[h], nil
}
func (m *fanoutMock) ListFeedEntries(_ context.Context, h string, since time.Time, limit int) ([]model.UpdateFeedEntry, error) {
	m.entryCalls[h]++
	m.lastSince[h] = since
	var out []model.UpdateFeedEntry
	for _, e := range m.entries[h] {
		if e.SeenAt.After(since) {
			out = append(out, e)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (m *fanoutMock) SetFeedSubFannedAt(_ context.Context, sub model.UpdateFeedSub, at time.Time) error {
	m.fannedAt[sub.UserID] = at
	return nil
}
func (m *fanoutMock) PutItemsIfAbsent(_ context.Context, userID, _ string, items []model.UpdateItem) (int, error) {
	if err := m.putErr[userID]; err != nil {
		return 0, err
	}
	n := 0
	for _, it := range items {
		key := userID + "|" + it.URLHash
		if m.existingURLs[key] {
			continue
		}
		m.existingURLs[key] = true
		n++
	}
	return n, nil
}
func (m *fanoutMock) EnsureURLPending(_ context.Context, _, url string) error {
	m.pendingURLs = append(m.pendingURLs, url)
	return nil
}
func (m *fanoutMock) MarkPending(_ context.Context, userID string) error {
	m.marked = append(m.marked, userID)
	return m.markErr
}

var fanoutNow = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

func fanoutSvc(m *fanoutMock) *CustomerUpdateFanout {
	return NewCustomerUpdateFanout(m, func() time.Time { return fanoutNow })
}

func fanoutSub(feed, user string) model.UpdateFeedSub {
	return model.UpdateFeedSub{FeedHash: feed, UserID: user, SourceID: "src-" + user, Status: model.UpdateSourceActive}
}

func fanoutEntry(i int, seen time.Time, url string) model.UpdateFeedEntry {
	return model.UpdateFeedEntry{EntryHash: fmt.Sprintf("e%03d", i), URL: url, SeenAt: seen}
}

func TestFanout_UnchangedFeedSkipsReads(t *testing.T) {
	m := newFanoutMock()
	s := fanoutSub("feedaaaaaaaaaaaaaa", "u1")
	s.FannedAt = fanoutNow.Add(-time.Hour)
	m.pages[""] = []model.UpdateFeedSub{s}
	m.states[s.FeedHash] = &model.UpdateFeedState{FeedHash: s.FeedHash, UpdatedAt: s.FannedAt}
	st, err := fanoutSvc(m).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st != (FanoutStats{Subs: 1, Skipped: 1}) {
		t.Fatalf("stats = %+v", st)
	}
	if m.entryCalls[s.FeedHash] != 0 || len(m.fannedAt) != 0 {
		t.Fatal("unchanged feed must not read entries or set FannedAt")
	}
}

func TestFanout_DeliversAndRejects(t *testing.T) {
	m := newFanoutMock()
	s := fanoutSub("feedaaaaaaaaaaaaaa", "u1")
	s.FannedAt = fanoutNow.Add(-time.Hour)
	m.pages[""] = []model.UpdateFeedSub{s}
	m.states[s.FeedHash] = &model.UpdateFeedState{UpdatedAt: fanoutNow.Add(-time.Minute)}
	seen := fanoutNow.Add(-10 * time.Minute)
	m.entries[s.FeedHash] = []model.UpdateFeedEntry{
		fanoutEntry(1, seen, "https://example.com/a"),
		fanoutEntry(2, seen.Add(time.Second), "http://localhost/internal"),
		fanoutEntry(3, seen.Add(2*time.Second), "https://example.com/b"),
	}
	st, err := fanoutSvc(m).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st != (FanoutStats{Subs: 1, Delivered: 2, Rejected: 1}) {
		t.Fatalf("stats = %+v", st)
	}
	if len(m.marked) != 1 || len(m.pendingURLs) != 2 {
		t.Fatalf("marked=%v pendingURLs=%v", m.marked, m.pendingURLs)
	}
	if !m.fannedAt["u1"].Equal(fanoutNow) {
		t.Fatalf("fannedAt = %v", m.fannedAt["u1"])
	}
}

func TestFanout_DuplicateItemsStillMarkPending(t *testing.T) {
	m := newFanoutMock()
	s := fanoutSub("feedaaaaaaaaaaaaaa", "u1")
	m.pages[""] = []model.UpdateFeedSub{s}
	m.states[s.FeedHash] = &model.UpdateFeedState{UpdatedAt: fanoutNow.Add(-time.Minute)}
	m.entries[s.FeedHash] = []model.UpdateFeedEntry{fanoutEntry(1, fanoutNow.Add(-time.Hour), "https://example.com/a")}
	f := fanoutSvc(m)
	if _, err := f.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Second run over the same entry: the item exists, nothing is created.
	m.marked = nil
	st, err := f.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Delivered != 0 || len(m.marked) != 1 {
		t.Fatalf("stats=%+v marked=%v", st, m.marked)
	}
}

func TestFanout_LostMarkerHealedOnRetry(t *testing.T) {
	m := newFanoutMock()
	s := fanoutSub("feedaaaaaaaaaaaaaa", "u1")
	m.pages[""] = []model.UpdateFeedSub{s}
	m.states[s.FeedHash] = &model.UpdateFeedState{UpdatedAt: fanoutNow.Add(-time.Minute)}
	m.entries[s.FeedHash] = []model.UpdateFeedEntry{fanoutEntry(1, fanoutNow.Add(-time.Hour), "https://example.com/a")}
	f := fanoutSvc(m)
	m.markErr = errors.New("mark failed")
	if _, err := f.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.fannedAt["u1"]; ok {
		t.Fatal("FannedAt must stay unchanged when MarkPending fails")
	}
	// Retry: the item already exists (duplicate) but the marker is healed.
	m.markErr = nil
	m.marked = nil
	st, err := f.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Delivered != 0 || len(m.marked) != 1 || !m.fannedAt["u1"].Equal(fanoutNow) {
		t.Fatalf("stats=%+v marked=%v fannedAt=%v", st, m.marked, m.fannedAt)
	}
}

func TestFanout_DisabledSubOnSameFeedGetsNothing(t *testing.T) {
	m := newFanoutMock()
	d, a := fanoutSub("feedaaaaaaaaaaaaaa", "u1"), fanoutSub("feedaaaaaaaaaaaaaa", "u2")
	d.Status = model.UpdateSourceDisabled
	m.pages[""] = []model.UpdateFeedSub{d, a}
	m.states[a.FeedHash] = &model.UpdateFeedState{UpdatedAt: fanoutNow.Add(-time.Minute)}
	m.entries[a.FeedHash] = []model.UpdateFeedEntry{fanoutEntry(1, fanoutNow.Add(-time.Hour), "https://example.com/a")}
	st, err := fanoutSvc(m).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.fannedAt["u1"]; ok {
		t.Fatal("disabled sub must not get FannedAt")
	}
	for k := range m.existingURLs {
		if k[:2] == "u1" {
			t.Fatalf("disabled sub received item %s", k)
		}
	}
	if st.Subs != 2 || st.Skipped != 1 || st.Delivered != 1 || !m.fannedAt["u2"].Equal(fanoutNow) {
		t.Fatalf("stats=%+v fannedAt=%v", st, m.fannedAt)
	}
}

func TestFanout_PutErrorKeepsFannedAtAndContinues(t *testing.T) {
	m := newFanoutMock()
	s1, s2 := fanoutSub("feedaaaaaaaaaaaaaa", "u1"), fanoutSub("feedaaaaaaaaaaaaaa", "u2")
	m.pages[""] = []model.UpdateFeedSub{s1, s2}
	m.states[s1.FeedHash] = &model.UpdateFeedState{UpdatedAt: fanoutNow.Add(-time.Minute)}
	m.entries[s1.FeedHash] = []model.UpdateFeedEntry{fanoutEntry(1, fanoutNow.Add(-time.Hour), "https://example.com/a")}
	m.putErr["u1"] = errors.New("boom")
	st, err := fanoutSvc(m).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.fannedAt["u1"]; ok {
		t.Fatal("failed subscription must keep FannedAt")
	}
	if !m.fannedAt["u2"].Equal(fanoutNow) || st.Delivered != 1 {
		t.Fatalf("second sub not processed: %+v %v", st, m.fannedAt)
	}
}

func TestFanout_DisabledAndMissingStateSkipped(t *testing.T) {
	m := newFanoutMock()
	d := fanoutSub("feeddddddddddddd", "u1")
	d.Status = model.UpdateSourceDisabled
	n := fanoutSub("feednnnnnnnnnnnnn", "u2")
	m.pages[""] = []model.UpdateFeedSub{d, n}
	m.states[d.FeedHash] = &model.UpdateFeedState{UpdatedAt: fanoutNow}
	st, err := fanoutSvc(m).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st != (FanoutStats{Subs: 2, Skipped: 2}) {
		t.Fatalf("stats = %+v", st)
	}
	if len(m.fannedAt) != 0 || m.entryCalls[d.FeedHash]+m.entryCalls[n.FeedHash] != 0 {
		t.Fatal("skipped rows must not read or write")
	}
}

func TestFanout_ZeroFannedAtReadsThreeDays(t *testing.T) {
	m := newFanoutMock()
	s := fanoutSub("feedaaaaaaaaaaaaaa", "u1")
	m.pages[""] = []model.UpdateFeedSub{s}
	m.states[s.FeedHash] = &model.UpdateFeedState{UpdatedAt: fanoutNow.Add(-time.Minute)}
	if _, err := fanoutSvc(m).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := fanoutNow.Add(-72 * time.Hour); !m.lastSince[s.FeedHash].Equal(want) {
		t.Fatalf("since = %v want %v", m.lastSince[s.FeedHash], want)
	}
}

func TestFanout_FullPageSetsFannedAtToLastEntry(t *testing.T) {
	m := newFanoutMock()
	s := fanoutSub("feedaaaaaaaaaaaaaa", "u1")
	s.FannedAt = fanoutNow.Add(-24 * time.Hour)
	m.pages[""] = []model.UpdateFeedSub{s}
	m.states[s.FeedHash] = &model.UpdateFeedState{UpdatedAt: fanoutNow.Add(-time.Minute)}
	base := fanoutNow.Add(-10 * time.Hour)
	var es []model.UpdateFeedEntry
	for i := 0; i < 60; i++ {
		es = append(es, fanoutEntry(i, base.Add(time.Duration(i)*time.Minute), fmt.Sprintf("https://example.com/p%d", i)))
	}
	m.entries[s.FeedHash] = es
	st, err := fanoutSvc(m).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Delivered != 50 {
		t.Fatalf("delivered = %d", st.Delivered)
	}
	if want := es[49].SeenAt; !m.fannedAt["u1"].Equal(want) {
		t.Fatalf("fannedAt = %v want %v", m.fannedAt["u1"], want)
	}
}

func TestFanout_SharedFeedReadsStateOnce(t *testing.T) {
	m := newFanoutMock()
	s1, s2 := fanoutSub("feedaaaaaaaaaaaaaa", "u1"), fanoutSub("feedaaaaaaaaaaaaaa", "u2")
	s1.FannedAt = fanoutNow.Add(-time.Hour)
	s2.FannedAt = fanoutNow.Add(-time.Hour)
	m.pages[""] = []model.UpdateFeedSub{s1, s2}
	m.states[s1.FeedHash] = &model.UpdateFeedState{UpdatedAt: fanoutNow.Add(-time.Minute)}
	m.entries[s1.FeedHash] = []model.UpdateFeedEntry{fanoutEntry(1, fanoutNow.Add(-time.Minute), "https://example.com/a")}
	st, err := fanoutSvc(m).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m.stateCalls[s1.FeedHash] != 1 || m.entryCalls[s1.FeedHash] != 1 {
		t.Fatalf("state calls %d entry calls %d", m.stateCalls[s1.FeedHash], m.entryCalls[s1.FeedHash])
	}
	if st.Delivered != 2 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestFanout_FollowsSubPagination(t *testing.T) {
	m := newFanoutMock()
	s1, s2 := fanoutSub("feedaaaaaaaaaaaaaa", "u1"), fanoutSub("feedbbbbbbbbbbbbbb", "u2")
	m.pages[""] = []model.UpdateFeedSub{s1}
	m.nextCursor[""] = "c1"
	m.pages["c1"] = []model.UpdateFeedSub{s2}
	st, err := fanoutSvc(m).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Subs != 2 {
		t.Fatalf("subs = %d", st.Subs)
	}
}

func TestFanout_ListErrorReturned(t *testing.T) {
	m := newFanoutMock()
	m.listErr = errors.New("list failed")
	if _, err := fanoutSvc(m).Run(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func tieEntries(counts map[time.Time]int) []model.UpdateFeedEntry {
	var times []time.Time
	for t := range counts {
		times = append(times, t)
	}
	sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })
	var es []model.UpdateFeedEntry
	for _, t := range times {
		for k := 0; k < counts[t]; k++ {
			es = append(es, fanoutEntry(len(es), t, fmt.Sprintf("https://example.com/p%d", len(es))))
		}
	}
	return es
}

func capSetup(es []model.UpdateFeedEntry) (*fanoutMock, model.UpdateFeedSub) {
	m := newFanoutMock()
	s := fanoutSub("feedaaaaaaaaaaaaaa", "u1")
	m.pages[""] = []model.UpdateFeedSub{s}
	m.states[s.FeedHash] = &model.UpdateFeedState{UpdatedAt: fanoutNow.Add(-time.Minute)}
	m.entries[s.FeedHash] = es
	return m, s
}

func TestFanout_TieGroupSpanningCapStaysWhole(t *testing.T) {
	t1, t2 := fanoutNow.Add(-2*time.Hour), fanoutNow.Add(-time.Hour)
	m, s := capSetup(tieEntries(map[time.Time]int{t1: 30, t2: 30}))
	m.states[s.FeedHash].UpdatedAt = t2
	f := fanoutSvc(m)
	st, err := f.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Delivered != 30 || !m.fannedAt["u1"].Equal(t1) {
		t.Fatalf("tick 1: %+v fannedAt=%v", st, m.fannedAt["u1"])
	}
	// Tick 2 with the stored FannedAt delivers the rest, nothing twice.
	s.FannedAt = m.fannedAt["u1"]
	m.pages[""] = []model.UpdateFeedSub{s}
	st, err = f.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Delivered != 30 || !m.fannedAt["u1"].Equal(fanoutNow) {
		t.Fatalf("tick 2: %+v fannedAt=%v", st, m.fannedAt["u1"])
	}
}

func TestFanout_CapBoundaryBetweenGroupsDeliversFifty(t *testing.T) {
	t1, t2 := fanoutNow.Add(-2*time.Hour), fanoutNow.Add(-time.Hour)
	m, _ := capSetup(tieEntries(map[time.Time]int{t1: 50, t2: 10}))
	st, err := fanoutSvc(m).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Delivered != 50 || !m.fannedAt["u1"].Equal(t1) {
		t.Fatalf("%+v fannedAt=%v", st, m.fannedAt["u1"])
	}
}

func TestFanout_SingleTieGroupOverCapDoesNotLoop(t *testing.T) {
	t1 := fanoutNow.Add(-time.Hour)
	m, _ := capSetup(tieEntries(map[time.Time]int{t1: 55}))
	st, err := fanoutSvc(m).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Delivered != 50 || !m.fannedAt["u1"].Equal(t1) {
		t.Fatalf("%+v fannedAt=%v", st, m.fannedAt["u1"])
	}
}
