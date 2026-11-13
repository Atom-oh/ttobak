package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

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

func TestHandle_TickRunsFanout(t *testing.T) {
	f := &fakeFanout{}
	fanout = f
	var ev Event
	if err := json.Unmarshal([]byte(`{"kind":"tick"}`), &ev); err != nil {
		t.Fatal(err)
	}
	if err := handle(context.Background(), ev); err != nil || f.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, f.calls)
	}
}

func TestHandle_TickReturnsFanoutError(t *testing.T) {
	fanout = &fakeFanout{err: errors.New("x")}
	if err := handle(context.Background(), Event{Kind: "tick"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestHandle_UnknownKind(t *testing.T) {
	f := &fakeFanout{}
	fanout = f
	if err := handle(context.Background(), Event{Kind: "x"}); err == nil || f.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, f.calls)
	}
}
