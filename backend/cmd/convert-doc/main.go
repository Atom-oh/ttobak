// convert-doc converts a PPTX/PPT slide upload to PDF using headless
// LibreOffice, so DocDetailClient.tsx can preview it the same way it already
// previews a native PDF upload. Triggered by an EventBridge S3 rule on the
// docs/ prefix (see infra/lib/gateway-stack.ts). Writes the result to a
// deterministic sidecar key (service.SidecarPDFKey) rather than touching
// DynamoDB -- the doc record may not exist yet at conversion time (the
// presigned PUT and the docApi.put() call that creates the record are two
// separate requests), and a deterministic key sidesteps that race entirely:
// the read path (UploadService.GeneratePreviewPDFURL) just HeadObjects the
// sidecar whenever it's asked, no ordering assumed.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

var (
	s3Client *s3.Client
	bucket   string
)

func init() {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("failed to load AWS config: %v", err)
	}
	s3Client = s3.NewFromConfig(cfg)
	bucket = os.Getenv("BUCKET_NAME")
	if bucket == "" {
		bucket = "ttobak-assets"
	}
	// LibreOffice needs a writable HOME for its profile dir; /tmp is the
	// only writable filesystem in the Lambda execution environment.
	os.Setenv("HOME", "/tmp")
}

func Handler(ctx context.Context, raw json.RawMessage) error {
	var event model.EventBridgeS3Event
	if err := json.Unmarshal(raw, &event); err != nil {
		return fmt.Errorf("failed to unmarshal EventBridge event: %w", err)
	}

	key := event.Detail.Object.Key
	if decoded, err := url.QueryUnescape(key); err == nil {
		key = decoded
	}

	sidecarKey := service.SidecarPDFKey(key)
	if sidecarKey == "" {
		log.Printf("skip: %s is not a docs/ PPTX/PPT upload", key)
		return nil
	}

	workDir, err := os.MkdirTemp("/tmp", "convert-doc-")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(workDir)

	inPath := filepath.Join(workDir, "in"+filepath.Ext(key))
	if err := downloadObject(ctx, key, inPath); err != nil {
		return fmt.Errorf("download %s: %w", key, err)
	}

	outDir := filepath.Join(workDir, "out")
	if err := os.Mkdir(outDir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	// soffice can hang on a malformed/hostile deck; bound it well under the
	// Lambda's own timeout so we always get a clean error instead of a
	// SIGKILL mid-write.
	convertCtx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(convertCtx, "soffice",
		"--headless", "--norestore", "--convert-to", "pdf", "--outdir", outDir, inPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("soffice convert failed: %w (output: %s)", err, string(out))
	}

	outPath := filepath.Join(outDir, strings.TrimSuffix(filepath.Base(inPath), filepath.Ext(inPath))+".pdf")
	if err := uploadObject(ctx, outPath, sidecarKey); err != nil {
		return fmt.Errorf("upload %s: %w", sidecarKey, err)
	}

	log.Printf("converted %s -> %s", key, sidecarKey)
	return nil
}

func downloadObject(ctx context.Context, key, destPath string) error {
	out, err := s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return err
	}
	defer out.Body.Close()

	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = io.Copy(f, out.Body)
	return err
}

func uploadObject(ctx context.Context, srcPath, key string) error {
	f, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        f,
		ContentType: aws.String("application/pdf"),
	})
	return err
}

func main() {
	lambda.Start(Handler)
}
