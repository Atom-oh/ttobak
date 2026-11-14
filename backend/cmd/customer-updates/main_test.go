package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/service"
)

type fakeFanout struct {
	calls int
	err   error
}

func (f *fakeFanout) Run(context.Context) (service.FanoutStats, error) {
	f.calls++
	return service.FanoutStats{}, f.err
}

type fakeRuns struct {
	calls    int
	err      error
	disabled bool
}

func (f *fakeRuns) Enabled() bool { return !f.disabled }

func (f *fakeRuns) Tick(context.Context) (service.RunStats, error) {
	f.calls++
	return service.RunStats{}, f.err
}

func TestHandle_TickRunsBothPhases(t *testing.T) {
	f, r := &fakeFanout{}, &fakeRuns{}
	fanout, runs = f, r
	var ev Event
	if err := json.Unmarshal([]byte(`{"kind":"tick"}`), &ev); err != nil {
		t.Fatal(err)
	}
	if err := handle(context.Background(), ev); err != nil || f.calls != 1 || r.calls != 1 {
		t.Fatalf("err=%v fanout=%d runs=%d", err, f.calls, r.calls)
	}
}

func TestHandle_TickJoinsErrors(t *testing.T) {
	fanErr, runErr := errors.New("fan"), errors.New("run")
	cases := []struct {
		name         string
		fanErr, rErr error
	}{
		{"fanout fails", fanErr, nil},
		{"runs fail", nil, runErr},
		{"both fail", fanErr, runErr},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, r := &fakeFanout{err: c.fanErr}, &fakeRuns{err: c.rErr}
			fanout, runs = f, r
			err := handle(context.Background(), Event{Kind: "tick"})
			if f.calls != 1 || r.calls != 1 {
				t.Fatalf("fanout=%d runs=%d: both phases must run", f.calls, r.calls)
			}
			for _, want := range []error{c.fanErr, c.rErr} {
				if want != nil && !errors.Is(err, want) {
					t.Fatalf("err = %v, want %v joined", err, want)
				}
			}
		})
	}
}

func TestHandle_NilRunsRunsFanoutOnly(t *testing.T) {
	f := &fakeFanout{}
	fanout, runs = f, nil
	if err := handle(context.Background(), Event{Kind: "tick"}); err != nil || f.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, f.calls)
	}
}

func TestFanoutContextKeepsRunPhaseTime(t *testing.T) {
	for _, c := range []struct {
		left, want time.Duration
	}{
		{14 * time.Minute, 14*time.Minute - 150*time.Second},
		{2 * time.Minute, 30 * time.Second},  // never less than 30 s of budget
		{10 * time.Second, 10 * time.Second}, // never after the invocation deadline
	} {
		parent, cancel := context.WithTimeout(context.Background(), c.left)
		ctx, done := fanoutContext(parent, true)
		deadline, ok := ctx.Deadline()
		got := time.Until(deadline)
		done()
		cancel()
		if !ok || got > c.want || got < c.want-2*time.Second {
			t.Fatalf("left %v: fan-out budget %v, want about %v", c.left, got, c.want)
		}
	}
	ctx, done := fanoutContext(context.Background(), true)
	defer done()
	if _, ok := ctx.Deadline(); ok {
		t.Fatal("no invocation deadline must add none")
	}
	parent, cancel := context.WithTimeout(context.Background(), 14*time.Minute)
	defer cancel()
	off, offDone := fanoutContext(parent, false)
	defer offDone()
	if d, _ := off.Deadline(); time.Until(d) < 14*time.Minute-2*time.Second {
		t.Fatalf("run phases off: fan-out budget %v, want the full deadline", time.Until(d))
	}
}

func TestHandle_DisabledRunsGiveFanoutFullDeadline(t *testing.T) {
	var left time.Duration
	fanout, runs = fanoutFunc(func(ctx context.Context) {
		d, _ := ctx.Deadline()
		left = time.Until(d)
	}), &fakeRuns{disabled: true}
	parent, cancel := context.WithTimeout(context.Background(), 14*time.Minute)
	defer cancel()
	if err := handle(parent, Event{Kind: "tick"}); err != nil || left < 14*time.Minute-2*time.Second {
		t.Fatalf("err=%v fan-out budget %v", err, left)
	}
}

type fanoutFunc func(ctx context.Context)

func (f fanoutFunc) Run(ctx context.Context) (service.FanoutStats, error) {
	f(ctx)
	return service.FanoutStats{}, nil
}

func TestHandle_UnknownKind(t *testing.T) {
	f, r := &fakeFanout{}, &fakeRuns{}
	fanout, runs = f, r
	if err := handle(context.Background(), Event{Kind: "x"}); err == nil || f.calls != 0 || r.calls != 0 {
		t.Fatalf("err=%v calls=%d/%d", err, f.calls, r.calls)
	}
}
