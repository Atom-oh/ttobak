// audio-remux writes ADR-014 seekable playback copies of browser-recorded
// meeting audio. It is triggered by EventBridge S3 Object Created events for
// WebM/Ogg keys under audio/ and can be invoked directly with {"key": "..."}
// by the backfill script. It never modifies the source object.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/ttobak/backend/internal/audioplayback"
	"github.com/ttobak/backend/internal/model"
)

var remuxer *audioplayback.Remuxer

func init() {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("failed to load AWS config: %v", err)
	}
	bucket := os.Getenv("BUCKET_NAME")
	if bucket == "" {
		log.Fatal("BUCKET_NAME environment variable is required")
	}
	remuxer = &audioplayback.Remuxer{
		S3:      s3.NewFromConfig(cfg),
		Bucket:  bucket,
		Tool:    audioplayback.FFmpegTool{FFmpegPath: envOr("FFMPEG_PATH", "/usr/bin/ffmpeg"), FFprobePath: envOr("FFPROBE_PATH", "/usr/bin/ffprobe")},
		WorkDir: "/tmp",
	}
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

type directRequest struct {
	Key string `json:"key"`
}

// eventKey accepts an EventBridge S3 event (literal, not URL-encoded key) or
// a direct {"key": ...} invocation.
func eventKey(raw json.RawMessage) (string, error) {
	var event model.EventBridgeS3Event
	if err := json.Unmarshal(raw, &event); err == nil && event.Detail.Object.Key != "" {
		return event.Detail.Object.Key, nil
	}
	var direct directRequest
	if err := json.Unmarshal(raw, &direct); err == nil && direct.Key != "" {
		return direct.Key, nil
	}
	return "", errors.New("event has no object key")
}

type response struct {
	Outcome audioplayback.Outcome `json:"outcome"`
}

func Handler(ctx context.Context, raw json.RawMessage) (response, error) {
	key, err := eventKey(raw)
	if err != nil {
		return response{}, err
	}
	// Leave headroom under the Lambda timeout for a clean error.
	if deadline, ok := ctx.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline.Add(-30*time.Second))
		defer cancel()
	}
	outcome, err := remuxer.Remux(ctx, key)
	if err != nil {
		// Object keys carry user/meeting IDs only; no meeting content is logged.
		return response{}, fmt.Errorf("remux %s: %w", key, err)
	}
	log.Printf("audio-remux outcome=%s key=%s", outcome, key)
	return response{Outcome: outcome}, nil
}

func main() {
	lambda.Start(Handler)
}
