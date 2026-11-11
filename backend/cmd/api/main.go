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
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	lambdasdk "github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/transcribe"
	"github.com/aws/aws-sdk-go-v2/service/translate"
	"github.com/awslabs/aws-lambda-go-api-proxy/chi"
	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/ttobak/backend/internal/connector"
	"github.com/ttobak/backend/internal/handler"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/sealing"
	"github.com/ttobak/backend/internal/service"
	"github.com/ttobak/backend/internal/telemetry"
)

var chiLambda *chiadapter.ChiLambda

// apiTelemetry is disabled (no-op) when no collector endpoint is configured.
var apiTelemetry = telemetry.Disabled()

func init() {
	// Load AWS config
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("failed to load AWS config: %v", err)
	}

	// Optional OpenTelemetry (disabled unless OTEL_EXPORTER_OTLP_ENDPOINT
	// is set). Each SDK client gets an operation-level span adapter that
	// records no request/response payloads.
	apiTelemetry = telemetry.FromEnv()
	awsTracing := apiTelemetry.AWSTracerProvider()

	// Initialize AWS clients
	dynamoClient := dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) { o.TracerProvider = awsTracing })
	s3Client := s3.NewFromConfig(cfg, func(o *s3.Options) { o.TracerProvider = awsTracing })
	ebClient := eventbridge.NewFromConfig(cfg, func(o *eventbridge.Options) { o.TracerProvider = awsTracing })
	bedrockAgentClient := bedrockagent.NewFromConfig(cfg, func(o *bedrockagent.Options) { o.TracerProvider = awsTracing })
	translateClient := translate.NewFromConfig(cfg, func(o *translate.Options) { o.TracerProvider = awsTracing })
	bedrockRuntimeClient2 := bedrockruntime.NewFromConfig(cfg, func(o *bedrockruntime.Options) { o.TracerProvider = awsTracing })
	transcribeClient := transcribe.NewFromConfig(cfg, func(o *transcribe.Options) { o.TracerProvider = awsTracing })
	kmsClient := kms.NewFromConfig(cfg, func(o *kms.Options) { o.TracerProvider = awsTracing })
	cognitoClient := cognitoidentityprovider.NewFromConfig(cfg, func(o *cognitoidentityprovider.Options) { o.TracerProvider = awsTracing })
	cognitoPoolID := os.Getenv("COGNITO_USER_POOL_ID")
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
	// Account contacts are third-party personal data: envelope-encrypted with
	// the KMS key, and unavailable (503) when it is not configured (ADR-021, contact encryption).
	if sealer := sealing.NewEnvelope(kmsClient, os.Getenv("KMS_KEY_ID")); sealer != nil {
		repo.SetRecordSealer(sealer)
	}
	connectorGrants := repo.ConnectorGrantResolver(os.Getenv("CONNECTOR_RESOURCE_AUDIENCE"),
		"https://cognito-idp."+cfg.Region+".amazonaws.com/"+cognitoPoolID)

	// Initialize services
	meetingService := service.NewMeetingService(repo)
	// Reuse the cold-start Cognito client for InviteUser/SearchUsers instead
	// of building one per call (see MeetingService.SetCognitoAdminAPI).
	meetingService.SetCognitoAdminAPI(cognitoClient, cognitoPoolID)
	userAdminService := service.NewUserAdminService(cognitoClient, cognitoPoolID, repo)
	accountService := service.NewAccountService(repo, s3Client, bucketName)
	calendarService := service.NewCalendarService(
		repository.NewCalendarRepository(dynamoClient, tableName),
		service.NewCalendarCipher(kmsClient, os.Getenv("KMS_KEY_ID")),
		accountService,
		repo,
	)
	calendarHandler := handler.NewCalendarHandler(calendarService)
	// Owner-private SA performance data: every route reads only the JWT user's
	// own USER# partition (ADR-023).
	performanceService := service.NewPerformanceService(repo, accountService)
	performanceService.SetMissedSources(calendarService, meetingService)
	performanceHandler := handler.NewPerformanceHandler(performanceService)
	calendarRepo := repository.NewCalendarRepository(dynamoClient, tableName)
	specReqTaskHandler := handler.NewSpecReqTaskHandler(service.NewSpecReqTaskService(
		repository.NewSpecReqTaskRepository(dynamoClient, tableName),
		calendarRepo,
		service.NewCalendarCipher(kmsClient, os.Getenv("KMS_KEY_ID")),
		accountService,
	))
	// Reuse the same cold-start Cognito client for AddMember's PendingShare
	// invited-check (see AccountService.SetCognitoAdminAPI).
	accountService.SetCognitoAdminAPI(cognitoClient, cognitoPoolID)
	accountService.SetInvitationCounter(service.NewWindowCounter(repository.NewRateCounterRepository(dynamoClient, tableName)))
	projectService := service.NewProjectService(repo)
	projectService.SetCognitoAdminAPI(cognitoClient, cognitoPoolID)
	vaultService := service.NewVaultService(repo)
	uploadService := service.NewUploadService(s3Client, repo, bucketName, ebClient)
	uploadService.SetAudioCropEnabled(os.Getenv("AUDIO_CROP_ENABLED") == "1")
	// Same-domain CloudFront-signed download URLs (ADR-027). Loaded once at
	// cold start in the background (two SSM reads, no longer on the init
	// path); a download request that arrives first waits for that load, and
	// any failure falls back to raw S3 presigns. The reload callback stays
	// registered so a warm instance also lazily retries -- a deploy that
	// races ahead of FrontendStack publishing the key-pair-id SSM param would
	// otherwise pin that instance to the S3 fallback until it's recycled.
	// Provisioned-concurrency init runs before any request, so its warm-ups
	// finish inside init; after init the environment is frozen until its
	// first request, which would stall background goroutines past their
	// deadlines and leave that request to do every lookup itself.
	provisioned := provisionedInit(os.Getenv)
	var signerLoaded <-chan struct{}
	if mediaBaseURL := os.Getenv("MEDIA_BASE_URL"); mediaBaseURL != "" {
		ssmClient := ssm.NewFromConfig(cfg, func(o *ssm.Options) { o.TracerProvider = awsTracing })
		signerLoaded = uploadService.StartCloudFrontSignerLoad(func(ctx context.Context) (*service.CloudFrontSigner, error) {
			return service.NewCloudFrontSigner(ctx, ssmClient, mediaBaseURL)
		}, 3*time.Second)
	}
	kbService := service.NewKBService(s3Client, bedrockAgentClient, kbBucketName, kbID, kbDataSourceID)
	kbService.SetAssetsBucketName(bucketName)
	kbService.SetMembershipReader(func(ctx context.Context, accountID, userID string) (bool, error) {
		member, err := repo.GetMember(ctx, accountID, userID)
		return member != nil, err
	})
	kbService.SetAccountKBRefWriter(repo.PutAccountKBRef)
	var indexStatusEngine *service.IndexingService
	if provider, err := service.NewIndexAWSProvider(s3Client, bedrockAgentClient, bucketName, kbBucketName, kbID, kbDataSourceID); err == nil {
		indexStatusEngine = service.NewIndexingService(repo.MetadataView(), provider, provider, bucketName)
	} else {
		log.Printf("warn: search status reader is not configured")
	}
	indexStatusHandler := handler.NewIndexStatusHandler(service.NewIndexStatusService(repo, indexStatusEngine))
	notionService := service.NewNotionService()
	// An empty BEDROCK_TRANSLATE_MODEL_ID keeps /api/translate on Amazon Translate.
	translateService := service.NewTranslateService(
		translateClient,
		service.WithInterpreterModel(bedrockRuntimeClient2, os.Getenv("BEDROCK_TRANSLATE_MODEL_ID")),
		service.WithInterpreterTimeout(service.ParseTranslateHighTimeout(os.Getenv("TRANSLATE_HIGH_TIMEOUT_MS"))),
	)
	// Initialize handlers
	healthHandler := handler.NewHealthHandler()
	meetingHandler := handler.NewMeetingHandler(meetingService, repo, uploadService)
	accountHandler := handler.NewAccountHandler(accountService, uploadService)
	accountMergeService := service.NewAccountMergeService(repo, accountService)
	accountMergeService.SetJobRepo(repo)
	// Start and resume hand the job to the summarize worker (GatewayStack rule).
	accountMergeService.SetMergePublisher(service.AccountMergePublisher(ebClient))
	accountMergeHandler := handler.NewAccountMergeHandler(accountMergeService)
	// Speaker map and contact changes queue a people resolve run in the
	// summarize worker (best-effort); the people panel reads metadata only.
	peopleResolvePublisher := service.PeopleResolvePublisher(ebClient)
	meetingService.SetPeopleResolvePublisher(peopleResolvePublisher)
	accountContactsService := service.NewAccountContactsService(repo)
	accountContactsService.SetPeopleResolvePublisher(peopleResolvePublisher)
	accountContactsHandler := handler.NewAccountContactsHandler(accountContactsService)
	accountPeopleService := service.NewAccountPeopleService(repo.MetadataView())
	accountPeopleService.SetPeopleResolvePublisher(peopleResolvePublisher)
	accountPeopleHandler := handler.NewAccountPeopleHandler(accountPeopleService)
	accountEventsHandler := handler.NewAccountEventsHandler(service.NewAccountEventsService(repo))
	accountReadingHandler := handler.NewAccountReadingHandler(service.NewAccountReadingService(repo.MetadataView()))
	meetingCatalogHandler := handler.NewMeetingCatalogHandler(service.NewMeetingCatalogService(repo.MetadataView()))
	vaultReadingHandler := handler.NewVaultReadingHandler(service.NewVaultReadingService(repo.MetadataView()))
	projectReadingHandler := handler.NewProjectReadingHandler(service.NewProjectReadingService(repo.MetadataView()))
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
	exportHandler.SetPDFStorage(uploadService)
	settingsHandler := handler.NewSettingsHandler(repo, cryptoService, notionService, meetingService)
	userAdminHandler := handler.NewUserAdminHandler(userAdminService)
	dictRepo := repository.NewDictionaryRepository(dynamoClient, tableName)
	dictService := service.NewDictionaryService(dictRepo, transcribeClient)
	// Only the API Lambda builds vocabularies (A/B slots, vocab/ table files).
	dictService.SetVocabularyBuilder(s3Client, bucketName, service.ParseVocabRebuildMinInterval(os.Getenv("VOCAB_REBUILD_MIN_INTERVAL")))
	dictService.SetMembershipReader(func(ctx context.Context, accountID, userID string) (bool, error) {
		member, err := repo.GetMember(ctx, accountID, userID)
		return member != nil, err
	})
	dictService.SetMembershipBatchReader(service.BatchMembershipReader(repo))
	// Account terms join the personal vocabulary in meeting-recency order.
	dictService.SetAccountOrder(service.AccountRecencyOrder(repo))
	dictHandler := handler.NewDictionaryHandler(dictService)
	translateHandler := handler.NewTranslateHandler(translateService)
	// Per-user hourly cost guard; 0 disables a limit, storage errors fail open.
	translateHandler.SetRateLimits(
		service.NewWindowCounter(repository.NewRateCounterRepository(dynamoClient, tableName)),
		handler.TranslateLimits{
			Hourly:     service.ParseRateLimit("TRANSLATE_HOURLY_LIMIT", os.Getenv("TRANSLATE_HOURLY_LIMIT"), handler.DefaultTranslateHourlyLimit),
			HighHourly: service.ParseRateLimit("TRANSLATE_HIGH_HOURLY_LIMIT", os.Getenv("TRANSLATE_HIGH_HOURLY_LIMIT"), handler.DefaultTranslateHighHourlyLimit),
		},
	)
	summarizeLiveHandler := handler.NewSummarizeLiveHandler(bedrockRuntimeClient2, meetingService)
	crawlerRepo := repository.NewCrawlerRepository(dynamoClient, tableName)
	crawlerService := service.NewCrawlerService(crawlerRepo)
	insightsService := service.NewInsightsService(crawlerRepo, s3Client, kbBucketName, bucketName)
	insightsService.SetAccountMemberReader(repo)
	crawlerHandler := handler.NewCrawlerHandler(crawlerService)
	accountNewsService := service.NewAccountNewsService(repo, crawlerRepo, insightsService)
	accountNewsHandler := handler.NewAccountNewsHandler(accountNewsService)
	accountLinksHandler := handler.NewAccountLinksHandler(service.NewAccountLinksService(repo, crawlerRepo, accountNewsService))
	accountOverviewHandler := handler.NewAccountOverviewHandler(service.NewAccountOverviewService(repo, crawlerRepo, accountNewsService, accountService))
	accountKBHandler := handler.NewAccountKBHandler(service.NewAccountKBService(repo, service.NewKBObjectStore(s3Client, kbBucketName), accountService, accountNewsService, crawlerRepo))
	insightsHandler := handler.NewInsightsHandler(insightsService, kbService)
	researchRepo := repository.NewResearchRepository(dynamoClient, tableName)
	sfnClient := sfn.NewFromConfig(cfg, func(o *sfn.Options) { o.TracerProvider = awsTracing })
	researchService := service.NewResearchService(researchRepo, repo, s3Client, sfnClient, kbBucketName, os.Getenv("RESEARCH_SFN_ARN"))
	researchHandler := handler.NewResearchHandler(researchService, notionService, repo, cryptoService)
	researchShareHandler := handler.NewResearchShareHandler(researchService)
	chatHandler := handler.NewChatHandler(repo)
	researchChatRepo := repository.NewChatRepository(dynamoClient, tableName)
	researchChatHandler := handler.NewResearchChatHandler(researchChatRepo, researchService)

	// Cost/sizing simulator (ADR-010). bedrockService reuses the api
	// Lambda's own Bedrock client (bedrockRuntimeClient2) for the
	// synchronous Haiku extraction call; the async run itself is handed off
	// to the ttobak-sim Lambda, not run inline here.
	bedrockService := service.NewBedrockService(bedrockRuntimeClient2, s3Client, repo)
	metadata := repo.MetadataView()
	attachmentTextService := service.NewAttachmentTextService(metadata, service.NewMeetingService(metadata), s3Client, bucketName, service.AttachmentTextPublisher(ebClient))
	attachmentTextHandler := handler.NewAttachmentTextHandler(attachmentTextService)
	meetingHandler.SetAttachmentTextService(attachmentTextService)
	bedrockService.SetAttachmentTextService(attachmentTextService)
	resummaryService := service.NewResummaryService(metadata, service.NewMeetingService(metadata), nil, nil, service.ResummaryPublisher(ebClient))
	resummaryHandler := handler.NewResummaryHandler(resummaryService)
	lambdaClient := lambdasdk.NewFromConfig(cfg, func(o *lambdasdk.Options) { o.TracerProvider = awsTracing })
	simFunctionName := os.Getenv("SIM_FUNCTION_NAME")
	if simFunctionName == "" {
		simFunctionName = "ttobak-sim"
	}
	simService := service.NewSimService(repo, bedrockService, lambdaClient, simFunctionName)
	simHandler := handler.NewSimHandler(simService)
	meetingHandler.SetSimService(simService)
	actionItemsService := service.NewMetadataActionItemsAnalysisService(repo, bedrockService, service.ActionItemsPublisher(ebClient))
	readingService := service.NewMeetingReadingService(repo, actionItemsService)
	readingHandler := handler.NewMeetingReadingHandler(readingService)
	// On-demand meeting translation (ADR-005): off unless exactly "true".
	meetingTranslationEnabled := os.Getenv("MEETING_TRANSLATION_ENABLED") == "true"
	meetingTranslationHandler := handler.NewMeetingTranslationHandler(service.NewMeetingTranslationService(
		readingService, translateService,
		service.NewWindowCounter(repository.NewRateCounterRepository(dynamoClient, tableName)),
		service.ParseRateLimit("MEETING_TRANSLATE_DAILY_BYTES", os.Getenv("MEETING_TRANSLATE_DAILY_BYTES"), service.DefaultMeetingTranslateDailyBytes),
	), meetingTranslationEnabled)
	meetingHandler.SetMeetingTranslationEnabled(meetingTranslationEnabled)
	navigationService := service.NewNavigationCatalogService(repo)
	accountCatalogHandler := handler.NewNavigationCatalogHandler(navigationService, "accounts")
	projectCatalogHandler := handler.NewNavigationCatalogHandler(navigationService, "projects")
	documentReadingService := service.NewDocumentReadingService(repo)
	documentReadingService.SetFileServices(accountService, uploadService)
	documentReadingHandler := handler.NewDocumentReadingHandler(documentReadingService)
	documentCatalogHandler := handler.NewDocumentCatalogHandler(service.NewDocumentCatalogService(repo))
	actionItemsHandler := handler.NewActionItemsHandler(actionItemsService)
	meetingHandler.SetActionItemsService(actionItemsService)
	sessionHandler := handler.NewSessionHandler(meetingService)
	billingInsightOrigin, err := service.ParseBillingInsightOrigin(os.Getenv("BILLING_INSIGHT_URL"))
	if err != nil {
		// Fail closed: the hand-off route answers 404 until the value is fixed.
		log.Printf("billing insight hand-off disabled: %v", err)
	}
	billingInsightHandler := handler.NewBillingInsightHandler(service.NewBillingInsightService(
		repository.NewBillingHandoffRepository(dynamoClient, tableName), billingInsightOrigin))
	connectorSettingsHandler := handler.NewConnectorSettingsHandler(service.NewConnectorSettingsService(repo, connectorSettingsConfig(cfg.Region, cognitoPoolID)))
	// Setup router
	r := chi.NewRouter()

	// Global middleware
	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.RealIP)
	r.Use(apiTelemetry.Middleware) // server span named by method + route pattern
	r.Use(middleware.RequestLog)
	r.Use(middleware.Recovery)
	originVerifier := middleware.NewOriginVerifier(
		os.Getenv("ORIGIN_VERIFY_SECRET_ARN"),
		secretsmanager.NewFromConfig(cfg, func(o *secretsmanager.Options) {
			o.TracerProvider = awsTracing
			o.HTTPClient = middleware.OriginSecretHTTPClient()
		}),
		os.Getenv("TTOBAK_ORIGIN_LOCAL_DEV") == "1" && os.Getenv("AWS_LAMBDA_FUNCTION_NAME") == "",
		os.Getenv("ORIGIN_VERIFY_SECRET_VERSION_ID"),
	)
	// Lambda only, so tests and local runs make no background network calls.
	if os.Getenv("AWS_LAMBDA_FUNCTION_NAME") != "" {
		if provisioned {
			started := time.Now()
			startColdStartWarmups(originVerifier, dynamoClient, cfg.Region, provisionedWarmupTimeout)()
			if signerLoaded != nil {
				<-signerLoaded
			}
			log.Printf("provisioned init warm-ups finished in %d ms", time.Since(started).Milliseconds())
		} else {
			startColdStartWarmups(originVerifier, dynamoClient, cfg.Region, coldStartPrefetchTimeout)
		}
	}
	r.Use(originVerifier.Middleware) // Block direct API Gateway access (CloudFront-only)
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
		r.Use(middleware.AuthWithConnectorGrants(middleware.ConnectorGrantResolver(connectorGrants)))
		r.Post("/api/session/bootstrap", sessionHandler.Bootstrap)
		r.Post("/api/billing-insight/handoff", billingInsightHandler.CreateHandoff)
		r.Post("/api/calendar/sources/{sourceId}/sync", calendarHandler.Sync)
		r.Get("/api/calendar/sources", calendarHandler.ListSources)
		r.Get("/api/calendar/events", calendarHandler.ListEvents)
		r.Post("/api/performance/sources/{source}/sync", performanceHandler.Sync)
		r.Get("/api/performance/sources", performanceHandler.ListSources)
		r.Get("/api/performance/goals", performanceHandler.GetGoals)
		r.Put("/api/performance/goals", performanceHandler.PutGoals)
		r.Get("/api/performance/mappings", performanceHandler.ListMappings)
		r.Put("/api/performance/mappings/{sfdcAccountId}", performanceHandler.PutMapping)
		r.Get("/api/performance/missed", performanceHandler.ListMissed)
		r.Get("/api/performance/reports", performanceHandler.ListReports)
		r.Get("/api/performance/reports/{periodKey}", performanceHandler.GetReport)
		r.Put("/api/performance/reports/{periodKey}", performanceHandler.PutReport)
		r.Delete("/api/performance/reports/{periodKey}", performanceHandler.DeleteReport)
		r.Get("/api/specreq-tasks", specReqTaskHandler.List)
		r.Get("/api/specreq-tasks/{eventId}", specReqTaskHandler.Get)
		r.Post("/api/specreq-tasks", specReqTaskHandler.Create)
		r.Post("/api/specreq-tasks/{eventId}/transitions", specReqTaskHandler.Transition)
		r.Put("/api/calendar/sources/{sourceId}/events/{eventId}/sharing", calendarHandler.SetEventSharing)
		r.Get("/api/calendar/share-directory", calendarHandler.ShareDirectory)
		r.Get("/api/accounts/{accountId}/calendar", calendarHandler.ListAccountCalendar)

		// Account routes
		r.Get("/api/accounts", accountHandler.ListAccounts)
		r.Get("/api/navigation/accounts", accountCatalogHandler.Get)
		r.Post("/api/accounts", accountHandler.CreateAccount)
		r.Post("/api/accounts/{accountId}/owner-invitation", accountHandler.ResendOwnerInvitation)
		r.Post("/api/accounts/{accountId}/owner-transfer", accountHandler.TransferOwner)
		r.Get("/api/accounts/{accountId}/merge-preview", accountMergeHandler.Preview)
		r.Post("/api/accounts/{accountId}/merge", accountMergeHandler.Start)
		r.Get("/api/accounts/{accountId}/merge", accountMergeHandler.Status)
		r.Get("/api/accounts/{accountId}", accountHandler.GetAccount)
		r.Get("/api/accounts/{accountId}/reading", accountReadingHandler.Get)
		r.Get("/api/accounts/{accountId}/contacts", accountContactsHandler.List)
		r.Post("/api/accounts/{accountId}/contacts", accountContactsHandler.Create)
		r.Put("/api/accounts/{accountId}/contacts/{contactId}", accountContactsHandler.Update)
		r.Delete("/api/accounts/{accountId}/contacts/{contactId}", accountContactsHandler.Delete)
		r.Get("/api/accounts/{accountId}/speaker-team", accountContactsHandler.SpeakerTeam)
		r.Get("/api/accounts/{accountId}/events", accountEventsHandler.List)
		r.Post("/api/accounts/{accountId}/events", accountEventsHandler.Create)
		r.Get("/api/accounts/{accountId}/events/{eventId}", accountEventsHandler.Get)
		r.Put("/api/accounts/{accountId}/events/{eventId}", accountEventsHandler.Update)
		r.Delete("/api/accounts/{accountId}/events/{eventId}", accountEventsHandler.Delete)
		r.Get("/api/accounts/{accountId}/news-sources", accountNewsHandler.List)
		r.Post("/api/accounts/{accountId}/news-sources", accountNewsHandler.Create)
		r.Delete("/api/accounts/{accountId}/news-sources/{sourceId}", accountNewsHandler.Delete)
		r.Get("/api/accounts/{accountId}/links", accountLinksHandler.List)
		r.Post("/api/accounts/{accountId}/links", accountLinksHandler.Create)
		r.Delete("/api/accounts/{accountId}/links", accountLinksHandler.Delete)
		r.Get("/api/accounts/{accountId}/people", accountPeopleHandler.List)
		r.Get("/api/accounts/{accountId}/people/mentions", accountPeopleHandler.Mentions)
		r.Post("/api/accounts/{accountId}/people/mentions/{mentionKey}/resolve", accountPeopleHandler.ResolveMention)
		r.Post("/api/accounts/{accountId}/people/mentions/{mentionKey}/ignore", accountPeopleHandler.IgnoreMention)
		r.Get("/api/accounts/{accountId}/people/edges", accountPeopleHandler.Edges)
		r.Post("/api/accounts/{accountId}/people/edges", accountPeopleHandler.Bind)
		r.Delete("/api/accounts/{accountId}/people/edges", accountPeopleHandler.Unbind)
		r.Post("/api/accounts/{accountId}/people/resolve", accountPeopleHandler.Resolve)
		r.Get("/api/accounts/{accountId}/signals", accountOverviewHandler.Signals)
		r.Get("/api/accounts/{accountId}/flow", accountOverviewHandler.Flow)
		r.Get("/api/accounts/{accountId}/graph", accountOverviewHandler.Graph)
		r.Get("/api/accounts/{accountId}/kb", accountKBHandler.List)
		r.Get("/api/accounts/{accountId}/kb/{refId}/download", accountKBHandler.Download)
		r.Delete("/api/accounts/{accountId}/kb/{refId}", accountKBHandler.Untag)
		r.Put("/api/accounts/{accountId}/parent", accountHandler.UpdateAccountParent)
		r.Post("/api/accounts/{accountId}/members", accountHandler.AddMember)
		r.Delete("/api/accounts/{accountId}/members/pending", accountHandler.RevokePendingMember)
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
		r.Get("/api/accounts/{accountId}/documents/{docId}/reading", documentReadingHandler.Get)
		r.Get("/api/accounts/{accountId}/documents/{docId}/index-status", indexStatusHandler.AccountDocument)
		r.Put("/api/accounts/{accountId}/documents/{docId}", accountHandler.UpdateDocument)
		r.Delete("/api/accounts/{accountId}/documents/{docId}", accountHandler.DeleteDocument)

		// Personal (account-less) document routes
		r.Post("/api/documents", documentHandler.PutDocument)
		r.Get("/api/documents", documentHandler.ListDocuments)
		r.Get("/api/document-catalog", documentCatalogHandler.Get)
		r.Get("/api/documents/{docId}", documentHandler.GetDocument)
		r.Get("/api/documents/{docId}/reading", documentReadingHandler.Get)
		r.Get("/api/documents/{docId}/index-status", indexStatusHandler.PersonalDocument)
		r.Put("/api/documents/{docId}", documentHandler.UpdateDocument)
		r.Delete("/api/documents/{docId}", documentHandler.DeleteDocument)
		r.Post("/api/documents/{docId}/share-account", documentHandler.ShareToAccount)
		r.Post("/api/documents/{docId}/share", documentHandler.ShareWithUser)
		r.Get("/api/documents/{docId}/shares", documentHandler.ListShares)
		r.Delete("/api/documents/{docId}/share/{userId}", documentHandler.RevokeShare)
		r.Post("/api/documents/{docId}/public-share", documentHandler.CreatePublicShare)
		r.Delete("/api/documents/{docId}/public-share", documentHandler.RevokePublicShare)
		r.Get("/api/vault/export", vaultHandler.ExportVault)
		r.Get("/api/vault/reading", vaultReadingHandler.Get)
		r.Post("/api/meetings/{meetingId}/account", meetingHandler.LinkToAccount)
		r.Put("/api/meetings/{meetingId}/category", meetingHandler.SetCategory)
		r.Post("/api/meetings/{meetingId}/share-account", shareHandler.ShareToAccount)

		// Meeting routes
		r.Get("/api/meetings", meetingHandler.ListMeetings)
		r.Get("/api/meeting-catalog", meetingCatalogHandler.Get)
		r.Post("/api/meetings", meetingHandler.CreateMeeting)
		r.Get("/api/meetings/{meetingId}", meetingHandler.GetMeeting)
		r.Get("/api/meetings/{meetingId}/reading", readingHandler.Get)
		r.Post("/api/meetings/{meetingId}/translation", meetingTranslationHandler.Post)
		r.Get("/api/meetings/{meetingId}/action-items", actionItemsHandler.Get)
		r.Get("/api/meetings/{meetingId}/resummary", resummaryHandler.Get)
		r.Post("/api/meetings/{meetingId}/resummary", resummaryHandler.Request)
		r.Get("/api/meetings/{meetingId}/notes/en", resummaryHandler.GetEnglish)
		r.Post("/api/meetings/{meetingId}/notes/en", resummaryHandler.RequestEnglish)
		r.Get("/api/meetings/{meetingId}/attachments/{attachmentId}/text/status", attachmentTextHandler.Status)
		r.Post("/api/meetings/{meetingId}/attachments/{attachmentId}/text/retry", attachmentTextHandler.Retry)
		r.Get("/api/meetings/{meetingId}/attachments/{attachmentId}/text", attachmentTextHandler.Read)
		r.Get("/api/meetings/{meetingId}/index-status", indexStatusHandler.Meeting)
		r.Post("/api/meetings/{meetingId}/action-items/retry", actionItemsHandler.Retry)
		r.Put("/api/meetings/{meetingId}/action-items/{itemId}", actionItemsHandler.SetCompleted)
		r.Put("/api/meetings/{meetingId}", meetingHandler.UpdateMeeting)
		r.Delete("/api/meetings/{meetingId}", meetingHandler.DeleteMeeting)

		// Audio playback
		r.Get("/api/meetings/{meetingId}/audio", meetingHandler.GetAudioURL)
		r.Post("/api/meetings/{meetingId}/audio/crop", meetingHandler.CropAudio)

		// Recording recovery (crashed browser)
		r.Post("/api/meetings/{meetingId}/recover", meetingHandler.RecoverMeeting)

		// Re-run speaker diarization with an updated speaker-count hint
		r.Post("/api/meetings/{meetingId}/rediarize", meetingHandler.RediarizeMeeting)

		// Cost/sizing simulator (ADR-010)
		r.Post("/api/meetings/{meetingId}/sim/extract", simHandler.ExtractRequirements)
		r.Post("/api/meetings/{meetingId}/sim", simHandler.CreateSimulation)

		// Transcript selection
		r.Put("/api/meetings/{meetingId}/transcript", meetingHandler.SelectTranscript)

		// Speaker mapping
		r.Put("/api/meetings/{meetingId}/speakers", meetingHandler.UpdateSpeakers)

		// Linked follow-up meetings
		r.Post("/api/meetings/{meetingId}/link", meetingHandler.LinkMeetings)

		// Share routes
		r.Post("/api/meetings/{meetingId}/share", shareHandler.ShareMeeting)
		r.Delete("/api/meetings/{meetingId}/share/pending", shareHandler.RevokePendingShare)
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
		r.Get("/api/kb/files/reading", kbHandler.ReadFiles)
		r.Delete("/api/kb/files/{fileId}", kbHandler.DeleteFile)

		// Q&A routes — migrated to Python Lambda (ttobak-qa)

		// Export routes
		r.Post("/api/meetings/{meetingId}/export", exportHandler.ExportMeeting)
		r.Get("/api/meetings/{meetingId}/export/obsidian", exportHandler.ExportObsidian)

		// Settings routes
		r.Get("/api/settings/integrations", settingsHandler.GetIntegrations)
		r.Get("/api/connectors", connectorSettingsHandler.List)
		r.Put("/api/connectors/approval", connectorSettingsHandler.UpdateApproval)
		r.Put("/api/settings/integrations/notion", settingsHandler.SaveNotionKey)
		r.Delete("/api/settings/integrations/notion", settingsHandler.DeleteNotionKey)

		// Allowed domains management
		r.Put("/api/settings/allowed-domains", settingsHandler.SaveAllowedDomains)

		// Admin-only routes
		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireAdmin)
			r.Put("/api/settings/connector-policy", connectorSettingsHandler.UpdatePolicy)
			r.Post("/api/settings/invite-user", settingsHandler.InviteUser)

			// Admin user-management panel (list/delete/enable/disable/resend-invite/reset-password)
			r.Get("/api/settings/users", userAdminHandler.ListUsers)
			r.Delete("/api/settings/users/{userId}", userAdminHandler.DeleteUser)
			r.Put("/api/settings/users/{userId}/enable", userAdminHandler.EnableUser)
			r.Put("/api/settings/users/{userId}/disable", userAdminHandler.DisableUser)
			r.Post("/api/settings/users/{userId}/resend-invite", userAdminHandler.ResendInvite)
			r.Post("/api/settings/users/{userId}/reset-password", userAdminHandler.ResetPassword)
		})

		// Dictionary routes
		r.Get("/api/settings/dictionary", dictHandler.GetDictionary)
		r.Put("/api/settings/dictionary", dictHandler.UpdateDictionary)
		r.Delete("/api/settings/dictionary/term", dictHandler.DeleteTerm)
		r.Post("/api/settings/dictionary/terms", dictHandler.CreateTerm)
		r.Patch("/api/settings/dictionary/terms/{termId}", dictHandler.UpdateTerm)
		r.Delete("/api/settings/dictionary/terms/{termId}", dictHandler.DeleteDictTerm)
		// Layered dictionary: {layer} is personal, common or account:{accountId}.
		r.Get("/api/settings/dictionary/layers/{layer}/terms", dictHandler.ListLayerTerms)
		r.Post("/api/settings/dictionary/layers/{layer}/terms", dictHandler.CreateLayerTerm)
		r.Patch("/api/settings/dictionary/layers/{layer}/terms/{termId}", dictHandler.UpdateLayerTerm)
		r.Delete("/api/settings/dictionary/layers/{layer}/terms/{termId}", dictHandler.DeleteLayerTerm)
		r.Get("/api/settings/dictionary/layers/{layer}/terms/{termId}/log", dictHandler.ListTermLog)
		r.Post("/api/settings/dictionary/layers/{layer}/terms/{termId}/log/{logId}/restore", dictHandler.RestoreTerm)

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
		r.Put("/api/research/{researchId}", researchHandler.UpdateResearch)
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
		r.Get("/api/navigation/projects", projectCatalogHandler.Get)
		r.Get("/api/projects/{projectId}", projectHandler.GetProject)
		r.Get("/api/projects/{projectId}/reading", projectReadingHandler.Get)
		r.Put("/api/projects/{projectId}", projectHandler.UpdateProject)
		r.Delete("/api/projects/{projectId}", projectHandler.DeleteProject)
		r.Post("/api/projects/{projectId}/members", projectHandler.AddMember)
		r.Get("/api/projects/{projectId}/members/pending", projectHandler.ListPendingMembers)
		r.Delete("/api/projects/{projectId}/members/pending", projectHandler.RevokePendingMember)
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
	if keyARN := os.Getenv("CONNECTOR_SIGNING_KEY_ARN"); keyARN != "" {
		connectorBridge = &connector.Bridge{
			Verify: lazyConnectorVerifier(kmsClient, keyARN, connectorGrants),
			Router: r,
			Mutate: handler.NewConnectorNoteHandler(service.NewConnectorNoteService(repo,
				os.Getenv("CONNECTOR_RESOURCE_AUDIENCE"), "https://cognito-idp."+cfg.Region+".amazonaws.com/"+cognitoPoolID)).Create,
			ContactMutate: handler.NewConnectorContactHandler(service.NewConnectorContactService(repo,
				os.Getenv("CONNECTOR_RESOURCE_AUDIENCE"), "https://cognito-idp."+cfg.Region+".amazonaws.com/"+cognitoPoolID)).Create,
			EventMutate: handler.NewConnectorEventHandler(service.NewConnectorEventService(repo,
				os.Getenv("CONNECTOR_RESOURCE_AUDIENCE"), "https://cognito-idp."+cfg.Region+".amazonaws.com/"+cognitoPoolID)).Create,
		}
	}
}

func main() {
	// Spans and logs are flushed (bounded by telemetry.FlushTimeout) before
	// each response returns, because a frozen Lambda cannot export later.
	lambda.Start(telemetry.WrapInvocation(apiTelemetry, dispatchAPI))
}
