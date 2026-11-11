package main

import (
	"context"
	"log"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecsTypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// whisperSpotCapacityProvider is the Spot pool every run used before the
// On-Demand provider existed; it remains the overflow and fallback pool.
const whisperSpotCapacityProvider = "ttobak-whisper-spot"

// onDemandAssociationTTL bounds how long a warm instance trusts its last
// observation of the cluster's capacity provider association.
const onDemandAssociationTTL = 5 * time.Minute

var (
	// whisperOnDemandProvider is WHISPER_ONDEMAND_CAPACITY_PROVIDER; empty
	// keeps every run on Spot.
	whisperOnDemandProvider string

	onDemandAssociation struct {
		checkedAt time.Time
		ok        bool
	}
	placementNow = time.Now
)

// chooseWhisperCapacityProvider places one Whisper run (ADR-009). A run that
// starts while the cluster has no task goes to the On-Demand provider, so the
// first active worker cannot be lost to Spot reclamation; runs that overlap
// it go to Spot. A single mixed Auto Scaling group cannot express this: ECS
// places the task on whichever host registers first, which left the
// On-Demand base host idle while the task died on its Spot sibling.
//
// Spot is used until the On-Demand provider is associated with the cluster,
// so this code can deploy before WhisperStack creates it. A failed task
// listing chooses On-Demand: that provider's group maximum bounds the cost of
// a wrong guess, while a wrong Spot guess can lose the meeting's transcript.
func chooseWhisperCapacityProvider(ctx context.Context, api ecsAPI) string {
	if whisperOnDemandProvider == "" || !onDemandProviderAssociated(ctx, api) {
		return whisperSpotCapacityProvider
	}
	out, err := api.ListTasks(ctx, &ecs.ListTasksInput{
		Cluster: aws.String(whisperCluster), DesiredStatus: ecsTypes.DesiredStatusRunning, MaxResults: aws.Int32(1),
	})
	if err != nil {
		log.Printf("whisper placement: task listing failed; using On-Demand")
		return whisperOnDemandProvider
	}
	if len(out.TaskArns) > 0 {
		return whisperSpotCapacityProvider
	}
	return whisperOnDemandProvider
}

// onDemandProviderAssociated reports whether the cluster lists the On-Demand
// provider. Lookup errors are not cached and fall back to Spot.
func onDemandProviderAssociated(ctx context.Context, api ecsAPI) bool {
	now := placementNow()
	if !onDemandAssociation.checkedAt.IsZero() && now.Sub(onDemandAssociation.checkedAt) < onDemandAssociationTTL {
		return onDemandAssociation.ok
	}
	out, err := api.DescribeClusters(ctx, &ecs.DescribeClustersInput{Clusters: []string{whisperCluster}})
	if err != nil {
		log.Printf("whisper placement: cluster lookup failed; using Spot")
		return false
	}
	ok := false
	for _, cluster := range out.Clusters {
		for _, provider := range cluster.CapacityProviders {
			ok = ok || provider == whisperOnDemandProvider
		}
	}
	onDemandAssociation.checkedAt, onDemandAssociation.ok = now, ok
	return ok
}
