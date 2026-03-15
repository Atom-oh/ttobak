package main

import (
	"context"
	"log"
	"os"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagent"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/translate"
	"github.com/awslabs/aws-lambda-go-api-proxy/chi"
	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/ttobak/backend/internal/handler"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

var chiLambda *chiadapter.ChiLambda

func init() {
	// Load AWS config
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("failed to load AWS config: %v", err)
	}

	// Initialize AWS clients
	dynamoClient := dynamodb.NewFromConfig(cfg)
	s3Client := s3.NewFromConfig(cfg)
	bedrockAgentClient := bedrockagent.NewFromConfig(cfg)
	translateClient := translate.NewFromConfig(cfg)
	bedrockRuntimeClient2 := bedrockruntime.NewFromConfig(cfg)
	ecsClient := ecs.NewFromConfig(cfg)

	// Get environment variables (per API spec: TABLE_NAME, BUCKET_NAME)
	tableName := os.Getenv("TABLE_NAME")
	if tableName == "" {
		tableName = "ttobak-main"
	}
	bucketName := os.Getenv("BUCKET_NAME")
	if bucketName == "" {
		bucketName = "ttobak-assets"
	}
	kbBucketName := os.Getenv("KB_BUCKET_NAME")
	if kbBucketName == "" {
		kbBucketName = "ttobak-kb"
	}
	kbID := os.Getenv("KB_ID")                     // Bedrock Knowledge Base ID
	kbDataSourceID := os.Getenv("KB_DATASOURCE_ID") // Bedrock Data Source ID

	// Realtime ECS environment variables
	ecsClusterName := os.Getenv("ECS_CLUSTER_NAME")
	if ecsClusterName == "" {
		ecsClusterName = "ttobak-realtime"
	}
	ecsServiceName := os.Getenv("ECS_SERVICE_NAME")
	if ecsServiceName == "" {
		ecsServiceName = "ttobak-realtime"
	}
	albDnsName := os.Getenv("ALB_DNS_NAME")
	if albDnsName == "" {
		albDnsName = "ttobak-realtime"
	}

	// Initialize repository
	repo := repository.NewDynamoDBRepository(dynamoClient, tableName)

	// Initialize services
	meetingService := service.NewMeetingService(repo)
	uploadService := service.NewUploadService(s3Client, repo, bucketName)
	kbService := service.NewKBService(s3Client, bedrockAgentClient, kbBucketName, kbID, kbDataSourceID)
	notionService := service.NewNotionService()
	translateService := service.NewTranslateService(translateClient)
	realtimeService := service.NewRealtimeService(ecsClient, ecsClusterName, ecsServiceName, albDnsName)

	// Initialize handlers
	healthHandler := handler.NewHealthHandler()
	meetingHandler := handler.NewMeetingHandler(meetingService, repo)
	shareHandler := handler.NewShareHandler(meetingService)
	uploadHandler := handler.NewUploadHandler(uploadService)
	kbHandler := handler.NewKBHandler(kbService)
	exportHandler := handler.NewExportHandler(meetingService, notionService, repo)
	settingsHandler := handler.NewSettingsHandler(repo)
	translateHandler := handler.NewTranslateHandler(translateService)
	summarizeLiveHandler := handler.NewSummarizeLiveHandler(bedrockRuntimeClient2)
	realtimeHandler := handler.NewRealtimeHandler(realtimeService)

	// Setup router
	r := chi.NewRouter()

	// Global middleware
	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.RealIP)
	r.Use(chimiddleware.Logger)
	r.Use(middleware.Recovery)
	r.Use(middleware.CORS)
	r.Use(middleware.JSON)

	// Health check (no auth required)
	r.Get("/api/health", healthHandler.Health)

	// Authenticated routes
	r.Group(func(r chi.Router) {
		r.Use(middleware.Auth)

		// Meeting routes
		r.Get("/api/meetings", meetingHandler.ListMeetings)
		r.Post("/api/meetings", meetingHandler.CreateMeeting)
		r.Get("/api/meetings/{meetingId}", meetingHandler.GetMeeting)
		r.Put("/api/meetings/{meetingId}", meetingHandler.UpdateMeeting)
		r.Delete("/api/meetings/{meetingId}", meetingHandler.DeleteMeeting)

		// Transcript selection
		r.Put("/api/meetings/{meetingId}/transcript", meetingHandler.SelectTranscript)

		// Share routes
		r.Post("/api/meetings/{meetingId}/share", shareHandler.ShareMeeting)
		r.Delete("/api/meetings/{meetingId}/share/{userId}", shareHandler.RevokeShare)

		// User search
		r.Get("/api/users/search", shareHandler.SearchUsers)

		// Upload routes
		r.Post("/api/upload/presigned", uploadHandler.GetPresignedURL)
		r.Post("/api/upload/complete", uploadHandler.UploadComplete)

		// KB routes
		r.Post("/api/kb/upload", kbHandler.GetPresignedURL)
		r.Post("/api/kb/sync", kbHandler.SyncKB)
		r.Get("/api/kb/files", kbHandler.ListFiles)
		r.Delete("/api/kb/files/{fileId}", kbHandler.DeleteFile)

		// Q&A routes — migrated to Python Lambda (ttobak-qa)

		// Export routes
		r.Post("/api/meetings/{meetingId}/export", exportHandler.ExportMeeting)
		r.Get("/api/meetings/{meetingId}/export/obsidian", exportHandler.ExportObsidian)

		// Settings routes
		r.Get("/api/settings/integrations", settingsHandler.GetIntegrations)
		r.Put("/api/settings/integrations/notion", settingsHandler.SaveNotionKey)
		r.Delete("/api/settings/integrations/notion", settingsHandler.DeleteNotionKey)

		// Translation route
		r.Post("/api/translate", translateHandler.Translate)

		// Live summarize route
		r.Post("/api/meetings/{meetingId}/summarize", summarizeLiveHandler.SummarizeLive)

		// Realtime STT routes
		r.Post("/api/realtime/start", realtimeHandler.StartRealtime)
		r.Post("/api/realtime/stop", realtimeHandler.StopRealtime)
	})

	chiLambda = chiadapter.New(r)
}

func main() {
	lambda.Start(chiLambda.ProxyWithContext)
}
