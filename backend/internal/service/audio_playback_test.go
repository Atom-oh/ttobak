package service

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type headReply struct {
	status          int
	etag, versionID string
	meta            map[string]string
}

func playbackUploadService(t *testing.T, replies map[string]headReply) (*UploadService, *[]string) {
	t.Helper()
	var heads []string
	client := s3.New(s3.Options{Region: "ap-northeast-2", Credentials: credentials.NewStaticCredentialsProvider("test-access-key", "test-secret", ""), RetryMaxAttempts: 1,
		BaseEndpoint: aws.String("http://storage.example.test"), UsePathStyle: true,
		HTTPClient: recoveryHTTPClient(func(r *http.Request) (*http.Response, error) {
			if r.Method != http.MethodHead {
				t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
			}
			key := strings.TrimPrefix(r.URL.Path, "/bucket/")
			heads = append(heads, key)
			reply, ok := replies[key]
			if !ok {
				reply = headReply{status: 404}
			}
			resp := recoveryResponse(reply.status, "")
			if reply.etag != "" {
				resp.Header.Set("ETag", reply.etag)
			}
			if reply.versionID != "" {
				resp.Header.Set("x-amz-version-id", reply.versionID)
			}
			for k, v := range reply.meta {
				resp.Header.Set("x-amz-meta-"+k, v)
			}
			return resp, nil
		})})
	return NewUploadService(client, nil, "bucket", nil), &heads
}

const (
	playbackSource = "audio/u1/m1/1793_recording.webm"
	playbackCopy   = "audio-playback/u1/m1/1793_recording.webm"
)

func TestPlaybackAudioKeySelectsOnlyACurrentCopy(t *testing.T) {
	current := map[string]string{"source-etag": `"e1"`, "source-version-id": "v1"}
	cases := []struct {
		name    string
		replies map[string]headReply
		want    string
	}{
		{"current copy", map[string]headReply{
			playbackSource: {status: 200, etag: `"e1"`, versionID: "v1"},
			playbackCopy:   {status: 200, etag: `"c1"`, meta: current},
		}, playbackCopy},
		{"no copy yet", map[string]headReply{
			playbackSource: {status: 200, etag: `"e1"`, versionID: "v1"},
		}, playbackSource},
		{"copy of an older version", map[string]headReply{
			playbackSource: {status: 200, etag: `"e2"`, versionID: "v2"},
			playbackCopy:   {status: 200, etag: `"c1"`, meta: current},
		}, playbackSource},
		{"copy without binding metadata", map[string]headReply{
			playbackSource: {status: 200, etag: `"e1"`, versionID: "v1"},
			playbackCopy:   {status: 200, etag: `"c1"`},
		}, playbackSource},
		{"copy lookup denied", map[string]headReply{
			playbackSource: {status: 200, etag: `"e1"`, versionID: "v1"},
			playbackCopy:   {status: 403},
		}, playbackSource},
		{"source lookup fails", map[string]headReply{
			playbackSource: {status: 500},
			playbackCopy:   {status: 200, etag: `"c1"`, meta: current},
		}, playbackSource},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := playbackUploadService(t, tc.replies)
			if got := svc.PlaybackAudioKey(context.Background(), playbackSource); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestPlaybackAudioKeySkipsNonCandidatesWithoutS3Calls(t *testing.T) {
	svc, heads := playbackUploadService(t, nil)
	for _, key := range []string{"audio/u1/m1/recording_progress.webm", "audio/u1/m1/a.m4a", "audio/u1/m1/crop_result_0123456789abcdef0123456789abcdef.wav"} {
		if got := svc.PlaybackAudioKey(context.Background(), key); got != key {
			t.Fatalf("%s -> %s", key, got)
		}
	}
	if len(*heads) != 0 {
		t.Fatalf("unexpected S3 calls %v", *heads)
	}
}

func TestGenerateAudioPlaybackURLSignsTheSelectedKey(t *testing.T) {
	svc, _ := playbackUploadService(t, map[string]headReply{
		playbackSource: {status: 200, etag: `"e1"`, versionID: "v1"},
		playbackCopy:   {status: 200, etag: `"c1"`, meta: map[string]string{"source-etag": `"e1"`, "source-version-id": "v1"}},
	})
	url, err := svc.GenerateAudioPlaybackURL(context.Background(), playbackSource)
	if err != nil || !strings.Contains(url, "/bucket/"+playbackCopy) {
		t.Fatalf("url %s err %v", url, err)
	}
}

func TestPlaybackAudioKeyFallsBackWhenTheLookupStalls(t *testing.T) {
	original := playbackLookupTimeout
	playbackLookupTimeout = 50 * time.Millisecond
	t.Cleanup(func() { playbackLookupTimeout = original })
	client := s3.New(s3.Options{Region: "ap-northeast-2", Credentials: credentials.NewStaticCredentialsProvider("test-access-key", "test-secret", ""), RetryMaxAttempts: 1,
		BaseEndpoint: aws.String("http://storage.example.test"), UsePathStyle: true,
		HTTPClient: recoveryHTTPClient(func(r *http.Request) (*http.Response, error) {
			<-r.Context().Done() // never answers
			return nil, r.Context().Err()
		})})
	svc := NewUploadService(client, nil, "bucket", nil)
	start := time.Now()
	if got := svc.PlaybackAudioKey(context.Background(), playbackSource); got != playbackSource {
		t.Fatalf("got %s", got)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("lookup took %s", elapsed)
	}
}
