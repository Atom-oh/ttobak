package main

import (
	"context"
	"log"
	"os"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagent"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/kms"
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
	ebClient := eventbridge.NewFromConfig(cfg)
	bedrockAgentClient := bedrockagent.NewFromConfig(cfg)
	translateClient := translate.NewFromConfig(cfg)
	bedrockRuntimeClient2 := bedrockruntime.NewFromConfig(cfg)
	kmsClient := kms.NewFromConfig(cfg)
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

	// Initialize repository with S3 support for large transcript storage
	repo := repository.NewDynamoDBRepositoryWithS3(dynamoClient, tableName, s3Client, bucketName)

	// Initialize services
	meetingService := service.NewMeetingService(repo)
	uploadService := service.NewUploadService(s3Client, repo, bucketName, ebClient)
	kbService := service.NewKBService(s3Client, bedrockAgentClient, kbBucketName, kbID, kbDataSourceID)
	kbService.SetAssetsBucketName(bucketName)
	notionService := service.NewNotionService()
	translateService := service.NewTranslateService(translateClient)
	// Initialize handlers
	healthHandler := handler.NewHealthHandler()
	meetingHandler := handler.NewMeetingHandler(meetingService, repo, uploadService)
	shareHandler := handler.NewShareHandler(meetingService)
	uploadHandler := handler.NewUploadHandler(uploadService)
	kbHandler := handler.NewKBHandler(kbService)
	exportHandler := handler.NewExportHandler(meetingService, notionService, repo)
	// Initialize crypto service for API key encryption (optional — requires KMS_KEY_ID)
	var cryptoService *service.CryptoService
	if kmsKeyID := os.Getenv("KMS_KEY_ID"); kmsKeyID != "" {
		cryptoService = service.NewCryptoService(kmsClient, kmsKeyID)
	}
	settingsHandler := handler.NewSettingsHandler(repo, cryptoService)
	translateHandler := handler.NewTranslateHandler(translateService)
	summarizeLiveHandler := handler.NewSummarizeLiveHandler(bedrockRuntimeClient2)
	crawlerRepo := repository.NewCrawlerRepository(dynamoClient, tableName)
	crawlerService := service.NewCrawlerService(crawlerRepo)
	insightsService := service.NewInsightsService(crawlerRepo, s3Client, kbBucketName)
	crawlerHandler := handler.NewCrawlerHandler(crawlerService)
	insightsHandler := handler.NewInsightsHandler(insightsService)
	researchRepo := repository.NewResearchRepository(dynamoClient, tableName)
	agentRuntimeClient := bedrockagentruntime.NewFromConfig(cfg)
	researchService := service.NewResearchService(researchRepo, s3Client, agentRuntimeClient, kbBucketName, os.Getenv("RESEARCH_AGENT_ID"), os.Getenv("RESEARCH_AGENT_ALIAS_ID"))
	researchHandler := handler.NewResearchHandler(researchService)
	chatHandler := handler.NewChatHandler(repo)
	// Setup router
	r := chi.NewRouter()

	// Global middleware
	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.RealIP)
	r.Use(chimiddleware.Logger)
	r.Use(middleware.Recovery)
	r.Use(middleware.OriginVerify) // Block direct API Gateway access (CloudFront-only)
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

		// Audio playback
		r.Get("/api/meetings/{meetingId}/audio", meetingHandler.GetAudioURL)

		// Recording recovery (crashed browser)
		r.Post("/api/meetings/{meetingId}/recover", meetingHandler.RecoverMeeting)

		// Transcript selection
		r.Put("/api/meetings/{meetingId}/transcript", meetingHandler.SelectTranscript)

		// Speaker mapping
		r.Put("/api/meetings/{meetingId}/speakers", meetingHandler.UpdateSpeakers)

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
		r.Post("/api/kb/copy-attachment", kbHandler.CopyAttachment)
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

		// Crawler settings
		r.Get("/api/crawler/sources", crawlerHandler.ListSources)
		r.Post("/api/crawler/sources", crawlerHandler.AddSource)
		r.Put("/api/crawler/sources/{sourceId}", crawlerHandler.UpdateSource)
		r.Delete("/api/crawler/sources/{sourceId}", crawlerHandler.Unsubscribe)
		r.Get("/api/crawler/sources/{sourceId}/history", crawlerHandler.GetHistory)

		// Insights
		r.Get("/api/insights", insightsHandler.ListInsights)
		r.Get("/api/insights/{sourceId}/{docHash}", insightsHandler.GetDocumentContent)

		// Research
		r.Post("/api/research", researchHandler.CreateResearch)
		r.Get("/api/research", researchHandler.ListResearch)
		r.Get("/api/research/{researchId}", researchHandler.GetResearchDetail)
		r.Delete("/api/research/{researchId}", researchHandler.DeleteResearch)

		// Chat session routes
		r.Get("/api/chat/sessions", chatHandler.ListSessions)
		r.Delete("/api/chat/sessions/{sessionId}", chatHandler.DeleteSession)

	})

	chiLambda = chiadapter.New(r)
}

func main() {
	lambda.Start(chiLambda.ProxyWithContext)
}
