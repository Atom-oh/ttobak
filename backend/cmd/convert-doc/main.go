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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/ttobak/backend/internal/convertdoc"
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
		// Fail fast rather than silently targeting a placeholder bucket
		// name that doesn't exist in this account -- a wrong-but-non-empty
		// default here would make every conversion fail with an opaque S3
		// error instead of a clear one at cold start.
		log.Fatal("BUCKET_NAME environment variable is required")
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

	// Unlike the classic S3->SNS/SQS/Lambda event notification JSON (which
	// URL-encodes object.key, form-style, so cmd/transcribe|summarize|process-image
	// apply url.QueryUnescape to it), EventBridge's "S3 Object Created"
	// detail.object.key is NOT URL-encoded -- it's the literal key.
	// Decoding it here would corrupt a literal "+" in a filename to a
	// space (QueryUnescape's form-decoding semantics), permanently
	// breaking conversion for e.g. "C++ 소개.pptx".
	key := event.Detail.Object.Key

	sidecarKey := service.SidecarPDFKey(key)
	if sidecarKey == "" {
		log.Printf("skip: %s is not a docs/ PPTX/PPT upload", key)
		return nil
	}

	if !convertdoc.IsSlideExtension(key) {
		return fmt.Errorf("unsupported file extension for key %q", key)
	}
	ext := strings.ToLower(filepath.Ext(key))

	workDir, err := os.MkdirTemp("/tmp", "convert-doc-")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(workDir)

	inPath := filepath.Join(workDir, "in"+ext)
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
	// soffice parses an untrusted, attacker-controllable PPTX/PPT. Stripping
	// AWS_* env vars (inherited by default from exec.Command's nil Env)
	// closes the *accidental* leak path -- an env dump, crash log, or error
	// message that includes the process's own environment -- but is NOT a
	// real barrier against a determined exploit: a same-UID child can still
	// recover the parent Go process's env via /proc/<ppid>/environ, and
	// this does nothing about SSRF or local-file-read via a document's
	// linked/remote content. LibreOffice conversion of untrusted input is
	// an inherent RCE surface; see ADR-022's Consequences for the tracked
	// residual risk (this Lambda's docs/* IAM grant is bucket-wide across
	// all users, not scoped to the triggering key) and follow-up.
	cmd.Env = convertdoc.SanitizedEnv(os.Environ())
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Truncated: soffice's stdout/stderr on a malformed/hostile input
		// can echo fragments of the document (paths, embedded object
		// names) -- cap what lands in this error (and therefore
		// CloudWatch, via the Lambda runtime's own error logging) rather
		// than including it in full.
		return fmt.Errorf("soffice convert failed: %w (output: %s)", err, convertdoc.TruncateOutput(out, 2000))
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
