package audioplayback

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// MaxSourceBytes matches the audio crop source limit; larger recordings keep
// serving the original.
const MaxSourceBytes int64 = 2 << 30

// Outcome describes what one remux request did. Skips are not errors.
type Outcome string

const (
	OutcomePublished      Outcome = "published"
	OutcomeNotCandidate   Outcome = "not_candidate"
	OutcomeAlreadyCurrent Outcome = "already_current"
	OutcomeTooLarge       Outcome = "too_large"
	OutcomeSourceChanged  Outcome = "source_changed"
	OutcomeRaced          Outcome = "raced"
)

type S3API interface {
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

// Tool rewrites a container without re-encoding and reports the result's
// duration in seconds. The production implementation runs ffmpeg/ffprobe.
type Tool interface {
	Remux(ctx context.Context, inPath, outPath, format string) error
	Duration(ctx context.Context, filePath string) (float64, error)
}

type Remuxer struct {
	S3      S3API
	Bucket  string
	Tool    Tool
	WorkDir string // parent for per-request temp directories
}

type sourceVersion struct {
	etag, versionID, contentType string
	size                         int64
}

// Remux publishes a seekable copy of sourceKey bound to the source version
// that was actually downloaded.
func (r *Remuxer) Remux(ctx context.Context, sourceKey string) (Outcome, error) {
	playbackKey, ok := KeyFor(sourceKey)
	if !ok {
		return OutcomeNotCandidate, nil
	}
	source, err := r.headSource(ctx, sourceKey)
	if err != nil {
		return "", err
	}
	if source.size > MaxSourceBytes {
		return OutcomeTooLarge, nil
	}
	destETag, current, err := r.headPlayback(ctx, playbackKey, source)
	if err != nil {
		return "", err
	}
	if current {
		return OutcomeAlreadyCurrent, nil
	}

	dir, err := os.MkdirTemp(r.WorkDir, "audio-remux-")
	if err != nil {
		return "", fmt.Errorf("create work dir: %w", err)
	}
	defer os.RemoveAll(dir)
	ext := strings.ToLower(path.Ext(sourceKey))
	inPath, outPath := filepath.Join(dir, "in"+ext), filepath.Join(dir, "out"+ext)

	if err := r.download(ctx, sourceKey, source, inPath); err != nil {
		if errors.Is(err, errVersionMismatch) {
			return OutcomeSourceChanged, nil
		}
		return "", fmt.Errorf("download source: %w", err)
	}
	format := "webm"
	if ext == ".ogg" {
		format = "ogg"
	}
	if err := r.Tool.Remux(ctx, inPath, outPath, format); err != nil {
		return "", fmt.Errorf("remux: %w", err)
	}
	seconds, err := r.Tool.Duration(ctx, outPath)
	if err != nil {
		return "", fmt.Errorf("probe remuxed audio: %w", err)
	}
	if !(seconds > 0) {
		return "", errors.New("remuxed audio has no duration")
	}

	// Publish only while the downloaded version is still current.
	latest, err := r.headSource(ctx, sourceKey)
	if err != nil {
		return "", err
	}
	if latest.etag != source.etag || latest.versionID != source.versionID {
		return OutcomeSourceChanged, nil
	}
	body, err := os.Open(outPath)
	if err != nil {
		return "", err
	}
	defer body.Close()
	input := &s3.PutObjectInput{
		Bucket: aws.String(r.Bucket), Key: aws.String(playbackKey), Body: body,
		ContentType: aws.String(source.contentType),
		Metadata:    map[string]string{MetaSourceETag: source.etag, MetaSourceVersionID: source.versionID},
	}
	if destETag == "" {
		input.IfNoneMatch = aws.String("*")
	} else {
		input.IfMatch = aws.String(destETag)
	}
	// A transport error can follow a successful PUT; never retry a stale precondition.
	if _, err := r.S3.PutObject(ctx, input, func(o *s3.Options) { o.Retryer = aws.NopRetryer{} }); err != nil {
		if isPreconditionFailed(err) {
			return OutcomeRaced, nil
		}
		return "", fmt.Errorf("publish playback copy: %w", err)
	}
	return OutcomePublished, nil
}

func (r *Remuxer) headSource(ctx context.Context, key string) (sourceVersion, error) {
	out, err := r.S3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(r.Bucket), Key: aws.String(key)})
	if err != nil {
		return sourceVersion{}, fmt.Errorf("head source: %w", err)
	}
	if out == nil || aws.ToString(out.ETag) == "" || aws.ToString(out.VersionId) == "" || out.ContentLength == nil || *out.ContentLength <= 0 {
		return sourceVersion{}, errors.New("source has no ETag, version or length")
	}
	contentType := aws.ToString(out.ContentType)
	if !strings.HasPrefix(contentType, "audio/") {
		contentType = "audio/webm"
		if strings.EqualFold(path.Ext(key), ".ogg") {
			contentType = "audio/ogg"
		}
	}
	return sourceVersion{etag: *out.ETag, versionID: *out.VersionId, contentType: contentType, size: *out.ContentLength}, nil
}

// headPlayback returns the current copy's ETag for a conditional replace and
// whether it already matches the source version.
func (r *Remuxer) headPlayback(ctx context.Context, key string, source sourceVersion) (string, bool, error) {
	out, err := r.S3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(r.Bucket), Key: aws.String(key)})
	var missing *types.NotFound
	if errors.As(err, &missing) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("head playback copy: %w", err)
	}
	if out == nil || aws.ToString(out.ETag) == "" {
		return "", false, errors.New("playback copy has no ETag")
	}
	return *out.ETag, MatchesSource(out.Metadata, source.etag, source.versionID), nil
}

var errVersionMismatch = errors.New("source version changed during download")

func (r *Remuxer) download(ctx context.Context, key string, source sourceVersion, target string) error {
	out, err := r.S3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(r.Bucket), Key: aws.String(key),
		VersionId: aws.String(source.versionID), IfMatch: aws.String(source.etag),
	})
	if err != nil {
		if isPreconditionFailed(err) {
			return errVersionMismatch
		}
		return err
	}
	if out == nil || out.Body == nil {
		return errors.New("source has no body")
	}
	defer out.Body.Close()
	if aws.ToString(out.ETag) != source.etag || aws.ToString(out.VersionId) != source.versionID {
		return errVersionMismatch
	}
	f, err := os.Create(target)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(f, io.LimitReader(out.Body, source.size+1))
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n != source.size {
		return errors.New("source length mismatch")
	}
	return nil
}

func isPreconditionFailed(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "PreconditionFailed", "ConditionalRequestConflict":
			return true
		}
	}
	return false
}
