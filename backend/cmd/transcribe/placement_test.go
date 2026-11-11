package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
)

const testOnDemandProvider = "ttobak-whisper-ondemand"

func resetPlacement(t *testing.T, onDemand string) {
	t.Helper()
	whisperCluster, whisperTaskDef, whisperContainer = "ttobak-whisper", "ttobak-whisper", "whisper"
	whisperOnDemandProvider = onDemand
	onDemandAssociation.checkedAt, onDemandAssociation.ok = time.Time{}, false
	placementNow = time.Now
	t.Cleanup(func() {
		whisperOnDemandProvider = ""
		onDemandAssociation.checkedAt, onDemandAssociation.ok = time.Time{}, false
		placementNow = time.Now
	})
}

func TestWhisperPlacementPutsTheFirstWorkerOnOnDemand(t *testing.T) {
	associated := []string{whisperSpotCapacityProvider, testOnDemandProvider}
	for _, tc := range []struct {
		name        string
		configured  string
		ecs         *fakeECS
		want        string
		wantListing bool
	}{
		{"unconfigured keeps Spot", "", &fakeECS{providers: associated}, whisperSpotCapacityProvider, false},
		{"provider not yet associated keeps Spot", testOnDemandProvider, &fakeECS{providers: []string{whisperSpotCapacityProvider}}, whisperSpotCapacityProvider, false},
		{"cluster lookup failure keeps Spot", testOnDemandProvider, &fakeECS{providers: associated, describeErr: errors.New("denied")}, whisperSpotCapacityProvider, false},
		{"idle cluster uses On-Demand", testOnDemandProvider, &fakeECS{providers: associated}, testOnDemandProvider, true},
		{"overlapping run uses Spot", testOnDemandProvider, &fakeECS{providers: associated, busy: true}, whisperSpotCapacityProvider, true},
		{"listing failure prefers On-Demand", testOnDemandProvider, &fakeECS{providers: associated, listErr: errors.New("throttled")}, testOnDemandProvider, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetPlacement(t, tc.configured)
			if got := chooseWhisperCapacityProvider(context.Background(), tc.ecs); got != tc.want {
				t.Fatalf("provider=%q, want %q", got, tc.want)
			}
			if (tc.ecs.lists > 0) != tc.wantListing {
				t.Fatalf("task listing calls=%d", tc.ecs.lists)
			}
		})
	}
}

func TestWhisperPlacementCachesOnlySuccessfulAssociationLookups(t *testing.T) {
	resetPlacement(t, testOnDemandProvider)
	now := time.Unix(1_000, 0)
	placementNow = func() time.Time { return now }
	api := &fakeECS{describeErr: errors.New("throttled")}
	chooseWhisperCapacityProvider(context.Background(), api)
	api.describeErr, api.providers = nil, []string{testOnDemandProvider}
	if got := chooseWhisperCapacityProvider(context.Background(), api); got != testOnDemandProvider || api.describes != 2 {
		t.Fatalf("lookup error was cached: provider=%q lookups=%d", got, api.describes)
	}
	api.providers = nil
	now = now.Add(onDemandAssociationTTL - time.Second)
	if got := chooseWhisperCapacityProvider(context.Background(), api); got != testOnDemandProvider || api.describes != 2 {
		t.Fatalf("association was not cached: provider=%q lookups=%d", got, api.describes)
	}
	now = now.Add(2 * time.Second)
	if got := chooseWhisperCapacityProvider(context.Background(), api); got != whisperSpotCapacityProvider || api.describes != 3 {
		t.Fatalf("expired association was trusted: provider=%q lookups=%d", got, api.describes)
	}
}

func TestWhisperRunTaskUsesTheChosenProvider(t *testing.T) {
	resetPlacement(t, "")
	for provider, want := range map[string]string{"": whisperSpotCapacityProvider, testOnDemandProvider: testOnDemandProvider} {
		in := whisperRunTaskInput("m", "u", "k", "", "", 0, whisperTaskOptions{CapacityProvider: provider})
		if len(in.CapacityProviderStrategy) != 1 || aws.ToString(in.CapacityProviderStrategy[0].CapacityProvider) != want ||
			in.CapacityProviderStrategy[0].Weight != 1 || in.CapacityProviderStrategy[0].Base != 0 {
			t.Fatalf("provider %q produced strategy %+v", provider, in.CapacityProviderStrategy)
		}
		if in.LaunchType != "" {
			t.Fatal("a capacity provider strategy excludes a launch type")
		}
	}
}

func TestRecoveryRedriveRunsOnTheOnDemandProvider(t *testing.T) {
	resetPlacement(t, testOnDemandProvider)
	h := newHarness(t, sttRecoveryModeActive, stuckRow(recM1))
	h.ecs.providers = []string{whisperSpotCapacityProvider, testOnDemandProvider}
	if err := h.runner.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.ecs.runs) != 1 {
		t.Fatalf("runs=%d", len(h.ecs.runs))
	}
	if got := aws.ToString(h.ecs.runs[0].CapacityProviderStrategy[0].CapacityProvider); got != testOnDemandProvider {
		t.Fatalf("recovery runs only on an idle cluster and must use On-Demand, got %q", got)
	}
}
