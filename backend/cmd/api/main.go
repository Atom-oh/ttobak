package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagent"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/transcribe"
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
	transcribeClient := transcribe.NewFromConfig(cfg)
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
	kbID := os.Getenv("KB_ID")                      // Bedrock Knowledge Base ID
	kbDataSourceID := os.Getenv("KB_DATASOURCE_ID") // Bedrock Data Source ID
	frontendBaseURL := os.Getenv("FRONTEND_BASE_URL")

	// Initialize repository with S3 support for large transcript storage
	repo := repository.NewDynamoDBRepositoryWithS3(dynamoClient, tableName, s3Client, bucketName)

	// Initialize services
	meetingService := service.NewMeetingService(repo)
	accountService := service.NewAccountService(repo, s3Client, bucketName)
	projectService := service.NewProjectService(repo)
	vaultService := service.NewVaultService(repo)
	uploadService := service.NewUploadService(s3Client, repo, bucketName, ebClient)
	// Same-domain CloudFront-signed download URLs (ADR-027). Tried once at
	// cold start; any failure falls back to raw S3 presigns. The reload
	// callback is registered unconditionally (not just on failure) so a warm
	// instance also lazily retries -- a deploy that races ahead of
	// FrontendStack publishing the key-pair-id SSM param would otherwise pin
	// that instance to the S3 fallback until it's recycled.
	if mediaBaseURL := os.Getenv("MEDIA_BASE_URL"); mediaBaseURL != "" {
		ssmClient := ssm.NewFromConfig(cfg)
		reload := func(ctx context.Context) (*service.CloudFrontSigner, error) {
			return service.NewCloudFrontSigner(ctx, ssmClient, mediaBaseURL)
		}
		coldStartCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		cfSigner, err := reload(coldStartCtx)
		cancel()
		if err != nil {
			log.Printf("warn: CloudFront signing unavailable, falling back to S3 presign (will retry lazily): %v", err)
		} else {
			uploadService.SetCloudFrontSigner(cfSigner)
		}
		uploadService.SetCloudFrontSignerReload(reload)
	}
	kbService := service.NewKBService(s3Client, bedrockAgentClient, kbBucketName, kbID, kbDataSourceID)
	kbService.SetAssetsBucketName(bucketName)
	notionService := service.NewNotionService()
	translateService := service.NewTranslateService(translateClient)
	// Initialize handlers
	healthHandler := handler.NewHealthHandler()
	meetingHandler := handler.NewMeetingHandler(meetingService, repo, uploadService)
	accountHandler := handler.NewAccountHandler(accountService, uploadService)
	projectHandler := handler.NewProjectHandler(projectService)
	documentHandler := handler.NewDocumentHandler(accountService, uploadService)
	vaultHandler := handler.NewVaultHandler(vaultService)
	shareHandler := handler.NewShareHandler(meetingService)
	uploadHandler := handler.NewUploadHandler(uploadService)
	kbHandler := handler.NewKBHandler(kbService)
	// Initialize crypto service for API key encryption (optional — requires KMS_KEY_ID)
	var cryptoService *service.CryptoService
	if kmsKeyID := os.Getenv("KMS_KEY_ID"); kmsKeyID != "" {
		cryptoService = service.NewCryptoService(kmsClient, kmsKeyID)
	}
	exportHandler := handler.NewExportHandler(meetingService, notionService, repo, cryptoService, frontendBaseURL)
	settingsHandler := handler.NewSettingsHandler(repo, cryptoService, notionService, meetingService)
	dictRepo := repository.NewDictionaryRepository(dynamoClient, tableName)
	dictService := service.NewDictionaryService(dictRepo, transcribeClient)
	dictHandler := handler.NewDictionaryHandler(dictService)
	translateHandler := handler.NewTranslateHandler(translateService)
	summarizeLiveHandler := handler.NewSummarizeLiveHandler(bedrockRuntimeClient2)
	crawlerRepo := repository.NewCrawlerRepository(dynamoClient, tableName)
	crawlerService := service.NewCrawlerService(crawlerRepo)
	insightsService := service.NewInsightsService(crawlerRepo, s3Client, kbBucketName)
	crawlerHandler := handler.NewCrawlerHandler(crawlerService)
	insightsHandler := handler.NewInsightsHandler(insightsService, kbService)
	researchRepo := repository.NewResearchRepository(dynamoClient, tableName)
	sfnClient := sfn.NewFromConfig(cfg)
	researchService := service.NewResearchService(researchRepo, repo, s3Client, sfnClient, kbBucketName, os.Getenv("RESEARCH_SFN_ARN"))
	researchHandler := handler.NewResearchHandler(researchService, notionService, repo, cryptoService)
	researchShareHandler := handler.NewResearchShareHandler(researchService)
	chatHandler := handler.NewChatHandler(repo)
	researchChatRepo := repository.NewChatRepository(dynamoClient, tableName)
	researchChatHandler := handler.NewResearchChatHandler(researchChatRepo, researchService)
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

	// Public: allowed domains (no auth required)
	r.Get("/api/auth/allowed-domains", settingsHandler.GetAllowedDomains)

	// Public: file-backed personal document share links -- any doc with a
	// FileKey, not just slides (no auth required -- see
	// DocumentHandler.PublicGetDoc's doc comment). Everything else document-
	// related lives inside the Auth group below; nothing added under
	// /api/public/ is ever authenticated, so keep this route surface minimal.
	r.Get("/api/public/docs/{token}", documentHandler.PublicGetDoc)

	// Authenticated routes
	r.Group(func(r chi.Router) {
		r.Use(middleware.Auth)

		// Account routes
		r.Get("/api/accounts", accountHandler.ListAccounts)
		r.Post("/api/accounts", accountHandler.CreateAccount)
		r.Get("/api/accounts/{accountId}", accountHandler.GetAccount)
		r.Post("/api/accounts/{accountId}/members", accountHandler.AddMember)
		r.Put("/api/accounts/{accountId}/members/{userId}", accountHandler.UpdateMemberRole)
		r.Delete("/api/accounts/{accountId}/members/{userId}", accountHandler.RemoveMember)
		r.Get("/api/accounts/{accountId}/meetings", accountHandler.ListAccountMeetings)
		r.Get("/api/accounts/{accountId}/insights", accountHandler.ListAccountInsights)
		r.Get("/api/accounts/{accountId}/brief", accountHandler.GetAccountBrief)
		r.Get("/api/accounts/{accountId}/research", researchHandler.ListAccountResearch)
		r.Get("/api/accounts/{accountId}/projects", projectHandler.ListAccountProjects)
		r.Post("/api/accounts/{accountId}/documents", accountHandler.PutDocument)
		r.Get("/api/accounts/{accountId}/documents", accountHandler.ListDocuments)
		r.Get("/api/accounts/{accountId}/documents/{docId}", accountHandler.GetDocument)
		r.Put("/api/accounts/{accountId}/documents/{docId}", accountHandler.UpdateDocument)
		r.Delete("/api/accounts/{accountId}/documents/{docId}", accountHandler.DeleteDocument)

		// Personal (account-less) document routes
		r.Post("/api/documents", documentHandler.PutDocument)
		r.Get("/api/documents", documentHandler.ListDocuments)
		r.Get("/api/documents/{docId}", documentHandler.GetDocument)
		r.Put("/api/documents/{docId}", documentHandler.UpdateDocument)
		r.Delete("/api/documents/{docId}", documentHandler.DeleteDocument)
		r.Post("/api/documents/{docId}/share-account", documentHandler.ShareToAccount)
		r.Post("/api/documents/{docId}/share", documentHandler.ShareWithUser)
		r.Get("/api/documents/{docId}/shares", documentHandler.ListShares)
		r.Delete("/api/documents/{docId}/share/{userId}", documentHandler.RevokeShare)
		r.Post("/api/documents/{docId}/public-share", documentHandler.CreatePublicShare)
		r.Delete("/api/documents/{docId}/public-share", documentHandler.RevokePublicShare)
		r.Get("/api/vault/export", vaultHandler.ExportVault)
		r.Post("/api/meetings/{meetingId}/account", meetingHandler.LinkToAccount)
		r.Post("/api/meetings/{meetingId}/share-account", shareHandler.ShareToAccount)

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

		// Re-run speaker diarization with an updated speaker-count hint
		r.Post("/api/meetings/{meetingId}/rediarize", meetingHandler.RediarizeMeeting)

		// Transcript selection
		r.Put("/api/meetings/{meetingId}/transcript", meetingHandler.SelectTranscript)

		// Speaker mapping
		r.Put("/api/meetings/{meetingId}/speakers", meetingHandler.UpdateSpeakers)

		// Linked follow-up meetings
		r.Post("/api/meetings/{meetingId}/link", meetingHandler.LinkMeetings)

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

		// Allowed domains management
		r.Put("/api/settings/allowed-domains", settingsHandler.SaveAllowedDomains)

		// Admin-only routes
		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireAdmin)
			r.Post("/api/settings/invite-user", settingsHandler.InviteUser)
		})

		// Dictionary routes
		r.Get("/api/settings/dictionary", dictHandler.GetDictionary)
		r.Put("/api/settings/dictionary", dictHandler.UpdateDictionary)
		r.Delete("/api/settings/dictionary/term", dictHandler.DeleteTerm)

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
		r.Delete("/api/insights/{sourceId}/{docHash}", insightsHandler.DeleteDocument)

		// Research
		r.Post("/api/research", researchHandler.CreateResearch)
		r.Get("/api/research", researchHandler.ListResearch)
		r.Get("/api/research/{researchId}", researchHandler.GetResearchDetail)
		r.Delete("/api/research/{researchId}", researchHandler.DeleteResearch)
		r.Post("/api/research/{researchId}/restore", researchHandler.RestoreResearch)
		r.Post("/api/research/{researchId}/export", researchHandler.ExportResearch)
		r.Post("/api/research/{researchId}/share", researchShareHandler.ShareResearch)
		r.Delete("/api/research/{researchId}/share/{userId}", researchShareHandler.RevokeResearchShare)
		r.Post("/api/research/{researchId}/accounts", researchHandler.LinkAccount)
		r.Delete("/api/research/{researchId}/accounts/{accountId}", researchHandler.UnlinkAccount)

		// Project routes
		r.Post("/api/projects", projectHandler.CreateProject)
		r.Get("/api/projects", projectHandler.ListMyProjects)
		r.Get("/api/projects/{projectId}", projectHandler.GetProject)
		r.Put("/api/projects/{projectId}", projectHandler.UpdateProject)
		r.Delete("/api/projects/{projectId}", projectHandler.DeleteProject)
		r.Post("/api/projects/{projectId}/members", projectHandler.AddMember)
		r.Delete("/api/projects/{projectId}/members/{userId}", projectHandler.RemoveMember)
		r.Post("/api/projects/{projectId}/accounts", projectHandler.LinkAccount)
		r.Delete("/api/projects/{projectId}/accounts/{accountId}", projectHandler.UnlinkAccount)
		r.Post("/api/projects/{projectId}/meetings", projectHandler.LinkMeeting)
		r.Delete("/api/projects/{projectId}/meetings/{meetingId}", projectHandler.UnlinkMeeting)
		r.Post("/api/projects/{projectId}/research", projectHandler.LinkResearch)
		r.Delete("/api/projects/{projectId}/research/{researchId}", projectHandler.UnlinkResearch)
		r.Get("/api/projects/{projectId}/meetings", projectHandler.ListProjectMeetings)
		r.Get("/api/projects/{projectId}/research", projectHandler.ListProjectResearch)
		r.Get("/api/projects/{projectId}/insights", projectHandler.GetProjectInsights)
		r.Get("/api/projects/{projectId}/brief", projectHandler.GetProjectBrief)

		// Research chat routes
		r.Get("/api/research/{researchId}/chat", researchChatHandler.ListMessages)
		r.Post("/api/research/{researchId}/chat", researchChatHandler.SendMessage)
		r.Get("/api/research/{researchId}/subpages", researchChatHandler.ListSubPages)

		// Chat session routes
		r.Get("/api/chat/sessions", chatHandler.ListSessions)
		r.Delete("/api/chat/sessions/{sessionId}", chatHandler.DeleteSession)

	})

	chiLambda = chiadapter.New(r)
}

func main() {
	lambda.Start(chiLambda.ProxyWithContext)
}
