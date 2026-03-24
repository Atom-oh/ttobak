package service

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// RealtimeService handles ECS service scaling for realtime STT
type RealtimeService struct {
	ecsClient   *ecs.Client
	clusterName string
	serviceName string
	albDnsName  string
}

// NewRealtimeService creates a new realtime service
func NewRealtimeService(ecsClient *ecs.Client, clusterName, serviceName, albDnsName string) *RealtimeService {
	return &RealtimeService{
		ecsClient:   ecsClient,
		clusterName: clusterName,
		serviceName: serviceName,
		albDnsName:  albDnsName,
	}
}

// StartRealtime sets ECS service desiredCount=1 and polls until a task is RUNNING.
// Returns the WebSocket URL for the client to connect.
// Timeout: 120 seconds.
func (s *RealtimeService) StartRealtime(ctx context.Context) (string, error) {
	// 1. Update ECS service desired count to 1
	_, err := s.ecsClient.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster:      aws.String(s.clusterName),
		Service:      aws.String(s.serviceName),
		DesiredCount: aws.Int32(1),
	})
	if err != nil {
		return "", fmt.Errorf("failed to update ECS service: %w", err)
	}

	// 2. Poll for task RUNNING status (max 120 seconds)
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		tasks, err := s.ecsClient.ListTasks(ctx, &ecs.ListTasksInput{
			Cluster:       aws.String(s.clusterName),
			ServiceName:   aws.String(s.serviceName),
			DesiredStatus: types.DesiredStatusRunning,
		})
		if err != nil {
			return "", fmt.Errorf("failed to list tasks: %w", err)
		}

		if len(tasks.TaskArns) > 0 {
			// Return relative path — frontend constructs full wss:// URL via CloudFront
			return "/ws", nil
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(5 * time.Second):
			// Continue polling
		}
	}

	return "", fmt.Errorf("timeout waiting for ECS task to start (120s)")
}

// StopRealtime sets ECS service desiredCount=0.
func (s *RealtimeService) StopRealtime(ctx context.Context) error {
	_, err := s.ecsClient.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster:      aws.String(s.clusterName),
		Service:      aws.String(s.serviceName),
		DesiredCount: aws.Int32(0),
	})
	if err != nil {
		return fmt.Errorf("failed to stop ECS service: %w", err)
	}
	return nil
}

// StartRealtimeAsync sets ECS desiredCount=1 without waiting. Returns immediately.
func (s *RealtimeService) StartRealtimeAsync(ctx context.Context) error {
	_, err := s.ecsClient.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster:      aws.String(s.clusterName),
		Service:      aws.String(s.serviceName),
		DesiredCount: aws.Int32(1),
	})
	if err != nil {
		return fmt.Errorf("failed to update ECS service: %w", err)
	}
	return nil
}

// GetStatus returns whether the ECS service has a task that is actually RUNNING and HEALTHY.
// Simply having a task ARN with DesiredStatus=RUNNING is not enough — we must check
// that lastStatus is RUNNING and healthStatus is HEALTHY before returning ready.
func (s *RealtimeService) GetStatus(ctx context.Context) (bool, string, error) {
	tasks, err := s.ecsClient.ListTasks(ctx, &ecs.ListTasksInput{
		Cluster:       aws.String(s.clusterName),
		ServiceName:   aws.String(s.serviceName),
		DesiredStatus: types.DesiredStatusRunning,
	})
	if err != nil {
		return false, "", fmt.Errorf("failed to list tasks: %w", err)
	}

	if len(tasks.TaskArns) == 0 {
		return false, "", nil
	}

	// Describe tasks to check actual status and health
	taskDetails, err := s.ecsClient.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(s.clusterName),
		Tasks:   tasks.TaskArns,
	})
	if err != nil {
		return false, "", fmt.Errorf("failed to describe tasks: %w", err)
	}

	for _, task := range taskDetails.Tasks {
		// Check that lastStatus is RUNNING (not PENDING, PROVISIONING, etc.)
		if task.LastStatus == nil || *task.LastStatus != "RUNNING" {
			continue
		}
		// Check that healthStatus is HEALTHY (not UNKNOWN or UNHEALTHY)
		// Note: healthStatus may be nil if no health check is configured
		if task.HealthStatus == types.HealthStatusHealthy {
			return true, "/ws", nil
		}
	}

	return false, "", nil
}
