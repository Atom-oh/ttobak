package audioplayback

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

type fakeObject struct {
	body      []byte
	etag      string
	versionID string
	meta      map[string]string
	ctype     string
}

type fakeS3 struct {
	objects map[string]*fakeObject
	// afterGet mutates state after the download, simulating a concurrent upload.
	afterGet func(f *fakeS3)
	puts     []*s3.PutObjectInput
	putErr   error
	// onPut runs before a failing PUT returns, simulating the concurrent winner.
	onPut func(f *fakeS3)
}

func (f *fakeS3) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	obj, ok := f.objects[aws.ToString(in.Key)]
	if !ok {
		return nil, &types.NotFound{}
	}
	return &s3.HeadObjectOutput{ETag: aws.String(obj.etag), VersionId: aws.String(obj.versionID),
		ContentLength: aws.Int64(int64(len(obj.body))), Metadata: obj.meta, ContentType: aws.String(obj.ctype)}, nil
}

func (f *fakeS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	obj, ok := f.objects[aws.ToString(in.Key)]
	if !ok {
		return nil, &types.NoSuchKey{}
	}
	if aws.ToString(in.IfMatch) != obj.etag || aws.ToString(in.VersionId) != obj.versionID {
		return nil, &smithy.GenericAPIError{Code: "PreconditionFailed"}
	}
	out := &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(obj.body)), ETag: aws.String(obj.etag), VersionId: aws.String(obj.versionID)}
	if f.afterGet != nil {
		f.afterGet(f)
	}
	return out, nil
}

func (f *fakeS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.puts = append(f.puts, in)
	if f.putErr != nil {
		if f.onPut != nil {
			f.onPut(f)
		}
		return nil, f.putErr
	}
	body, _ := io.ReadAll(in.Body)
	f.objects[aws.ToString(in.Key)] = &fakeObject{body: body, etag: `"new"`, versionID: "pv", meta: in.Metadata, ctype: aws.ToString(in.ContentType)}
	return &s3.PutObjectOutput{}, nil
}

type fakeTool struct {
	remuxed  []string
	duration float64
	err      error
}

func (t *fakeTool) Remux(_ context.Context, in, out, format string) error {
	if t.err != nil {
		return t.err
	}
	t.remuxed = append(t.remuxed, format)
	data, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	return os.WriteFile(out, append([]byte("remuxed:"), data...), 0o600)
}

func (t *fakeTool) Duration(context.Context, string) (float64, error) { return t.duration, nil }

const src = "audio/u1/m1/1793_recording_1793.webm"
const dst = "audio-playback/u1/m1/1793_recording_1793.webm"

func newFixture(t *testing.T) (*Remuxer, *fakeS3, *fakeTool) {
	t.Helper()
	s3fake := &fakeS3{objects: map[string]*fakeObject{
		src: {body: []byte("webm-bytes"), etag: `"e1"`, versionID: "v1", ctype: "audio/webm;codecs=opus"},
	}}
	tool := &fakeTool{duration: 12.5}
	return &Remuxer{S3: s3fake, Bucket: "b", Tool: tool, WorkDir: t.TempDir()}, s3fake, tool
}

func TestRemuxPublishesBoundCopy(t *testing.T) {
	r, s3fake, tool := newFixture(t)
	outcome, err := r.Remux(context.Background(), src)
	if err != nil || outcome != OutcomePublished {
		t.Fatalf("outcome=%s err=%v", outcome, err)
	}
	if len(s3fake.puts) != 1 {
		t.Fatalf("puts=%d", len(s3fake.puts))
	}
	put := s3fake.puts[0]
	if aws.ToString(put.Key) != dst || aws.ToString(put.IfNoneMatch) != "*" || put.IfMatch != nil {
		t.Fatalf("put key/condition: %s %v %v", aws.ToString(put.Key), put.IfNoneMatch, put.IfMatch)
	}
	if put.Metadata[MetaSourceETag] != `"e1"` || put.Metadata[MetaSourceVersionID] != "v1" {
		t.Fatalf("metadata %v", put.Metadata)
	}
	if aws.ToString(put.ContentType) != "audio/webm;codecs=opus" {
		t.Fatalf("content type %s", aws.ToString(put.ContentType))
	}
	if got := string(s3fake.objects[dst].body); got != "remuxed:webm-bytes" || tool.remuxed[0] != "webm" {
		t.Fatalf("body %q formats %v", got, tool.remuxed)
	}
	if string(s3fake.objects[src].body) != "webm-bytes" {
		t.Fatal("source was modified")
	}
	// A second run sees a current copy and does nothing.
	outcome, err = r.Remux(context.Background(), src)
	if err != nil || outcome != OutcomeAlreadyCurrent || len(s3fake.puts) != 1 {
		t.Fatalf("rerun outcome=%s err=%v puts=%d", outcome, err, len(s3fake.puts))
	}
}

func TestRemuxReplacesStaleCopyConditionally(t *testing.T) {
	r, s3fake, _ := newFixture(t)
	s3fake.objects[dst] = &fakeObject{body: []byte("old"), etag: `"old"`, versionID: "pv0",
		meta: map[string]string{MetaSourceETag: `"e0"`, MetaSourceVersionID: "v0"}}
	outcome, err := r.Remux(context.Background(), src)
	if err != nil || outcome != OutcomePublished {
		t.Fatalf("outcome=%s err=%v", outcome, err)
	}
	put := s3fake.puts[0]
	if aws.ToString(put.IfMatch) != `"old"` || put.IfNoneMatch != nil {
		t.Fatalf("replace must be IfMatch the observed copy: %v %v", put.IfMatch, put.IfNoneMatch)
	}
}

func TestRemuxSkips(t *testing.T) {
	r, s3fake, tool := newFixture(t)
	for _, key := range []string{"audio/u1/m1/recording_progress.webm", "audio/u1/m1/a.m4a", "images/u1/m1/a.webm"} {
		outcome, err := r.Remux(context.Background(), key)
		if err != nil || outcome != OutcomeNotCandidate {
			t.Fatalf("%s: outcome=%s err=%v", key, outcome, err)
		}
	}
	big := make([]byte, 0)
	s3fake.objects[src].body = big
	if _, err := r.Remux(context.Background(), src); err == nil {
		t.Fatal("empty source must fail")
	}
	if len(s3fake.puts) != 0 || len(tool.remuxed) != 0 {
		t.Fatal("skips must not remux or publish")
	}
}

func TestRemuxDoesNotPublishWhenSourceChanges(t *testing.T) {
	r, s3fake, _ := newFixture(t)
	s3fake.afterGet = func(f *fakeS3) {
		f.objects[src] = &fakeObject{body: []byte("newer"), etag: `"e2"`, versionID: "v2", ctype: "audio/webm"}
		f.afterGet = nil
	}
	outcome, err := r.Remux(context.Background(), src)
	if err != nil || outcome != OutcomeSourceChanged || len(s3fake.puts) != 0 {
		t.Fatalf("outcome=%s err=%v puts=%d", outcome, err, len(s3fake.puts))
	}
}

func TestRemuxReportsRaceAndFailures(t *testing.T) {
	r, s3fake, _ := newFixture(t)
	// Lost the race to a copy of the current source version: accepted.
	s3fake.putErr = &smithy.GenericAPIError{Code: "PreconditionFailed"}
	delete(s3fake.objects, dst)
	s3fake.onPut = func(f *fakeS3) {
		f.objects[dst] = &fakeObject{etag: `"winner"`, meta: map[string]string{MetaSourceETag: `"e1"`, MetaSourceVersionID: "v1"}}
	}
	if outcome, err := r.Remux(context.Background(), src); err != nil || outcome != OutcomeRaced {
		t.Fatalf("race outcome=%s err=%v", outcome, err)
	}
	// Lost the race to a copy of an older version: must error so the event retries.
	delete(s3fake.objects, dst)
	s3fake.onPut = func(f *fakeS3) {
		f.objects[dst] = &fakeObject{etag: `"stale"`, meta: map[string]string{MetaSourceETag: `"e0"`, MetaSourceVersionID: "v0"}}
	}
	if _, err := r.Remux(context.Background(), src); !errors.Is(err, errStaleAfterRace) {
		t.Fatalf("stale race err=%v", err)
	}
	s3fake.onPut = nil
	delete(s3fake.objects, dst)
	s3fake.putErr = errors.New("network")
	if _, err := r.Remux(context.Background(), src); err == nil {
		t.Fatal("publish failure must surface")
	}

	r2, s3fake2, tool2 := newFixture(t)
	tool2.err = errors.New("ffmpeg failed")
	if _, err := r2.Remux(context.Background(), src); err == nil || len(s3fake2.puts) != 0 {
		t.Fatalf("remux failure err=%v puts=%d", err, len(s3fake2.puts))
	}
	r3, s3fake3, tool3 := newFixture(t)
	tool3.duration = 0
	if _, err := r3.Remux(context.Background(), src); err == nil || len(s3fake3.puts) != 0 {
		t.Fatalf("zero duration err=%v puts=%d", err, len(s3fake3.puts))
	}
}

func TestRemuxArgsAndProbeParsing(t *testing.T) {
	args := strings.Join(RemuxArgs("/tmp/x/in.webm", "/tmp/x/out.webm", "webm"), " ")
	for _, want := range []string{"-protocol_whitelist file", "-format_whitelist matroska,webm,ogg", "-f matroska -i file:/tmp/x/in.webm", "-c copy", "-f webm -y file:/tmp/x/out.webm", "-nostdin"} {
		if !strings.Contains(args, want) {
			t.Errorf("args %q missing %q", args, want)
		}
	}
	if ogg := strings.Join(RemuxArgs("/a/in.ogg", "/a/out.ogg", "ogg"), " "); !strings.Contains(ogg, "-f ogg -i file:/a/in.ogg") {
		t.Errorf("ogg args %q", ogg)
	}
	probe := strings.Join(ProbeArgs("/a/out.webm", "matroska"), " ")
	for _, want := range []string{"-protocol_whitelist file", "-format_whitelist matroska,webm,ogg", "-f matroska", "file:/a/out.webm"} {
		if !strings.Contains(probe, want) {
			t.Errorf("probe %q missing %q", probe, want)
		}
	}
	if _, err := (FFmpegTool{}).Duration(context.Background(), "/a/out.mp4"); err == nil {
		t.Error("probe of an unpinned extension accepted")
	}
	if got, err := ParseProbeDuration([]byte("1689.537000\n")); err != nil || got != 1689.537 {
		t.Fatalf("parse %v %v", got, err)
	}
	for _, bad := range []string{"N/A", "", "-1", "0", "nan", "1e9"} {
		if _, err := ParseProbeDuration([]byte(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if (FFmpegTool{}).Remux(context.Background(), "a", "b", "mp4") == nil {
		t.Error("unsupported format accepted")
	}
	env := sanitizedEnv([]string{"AWS_SECRET_ACCESS_KEY=x", "AWS_REGION=y", "PATH=/bin"})
	if len(env) != 1 || env[0] != "PATH=/bin" {
		t.Fatalf("env %v", env)
	}
}

// TestFFmpegRejectsDisguisedLocalFileReader: a ".webm" that is really an
// ffconcat playlist must not pull another local file into the output.
func TestFFmpegRejectsDisguisedLocalFileReader(t *testing.T) {
	ffmpeg, err1 := exec.LookPath("ffmpeg")
	ffprobe, err2 := exec.LookPath("ffprobe")
	if err1 != nil || err2 != nil {
		t.Skip("ffmpeg/ffprobe not installed")
	}
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret.webm")
	gen := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "sine=duration=3", "-c:a", "libopus", "-f", "webm", "-y", secret)
	if err := gen.Run(); err != nil {
		t.Skipf("ffmpeg cannot encode opus here: %v", err)
	}
	disguised := filepath.Join(dir, "upload.webm")
	if err := os.WriteFile(disguised, []byte("ffconcat version 1.0\nfile 'secret.webm'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Without a pinned demuxer ffmpeg auto-detects ffconcat and copies the
	// relative sibling file; this guards that regression.
	tool := FFmpegTool{FFmpegPath: ffmpeg, FFprobePath: ffprobe}
	out := filepath.Join(dir, "out.webm")
	if err := tool.Remux(context.Background(), disguised, out, "webm"); err == nil {
		if seconds, probeErr := tool.Duration(context.Background(), out); probeErr == nil && seconds > 0 {
			t.Fatalf("disguised playlist produced %.1fs of another file's audio", seconds)
		}
	}
}

// TestFFmpegRepairsTruncatedLiveWebM checks the real tool on the browser case:
// a live (unknown-size, cue-less) WebM whose final cluster was cut off.
func TestFFmpegRepairsTruncatedLiveWebM(t *testing.T) {
	ffmpeg, err1 := exec.LookPath("ffmpeg")
	ffprobe, err2 := exec.LookPath("ffprobe")
	if err1 != nil || err2 != nil {
		t.Skip("ffmpeg/ffprobe not installed")
	}
	dir := t.TempDir()
	live := filepath.Join(dir, "live.webm")
	// Writing to a pipe makes the muxer behave like MediaRecorder: no Cues, no Duration.
	gen := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=20",
		"-c:a", "libopus", "-f", "webm", "pipe:1")
	out, err := gen.Output()
	if err != nil {
		t.Skipf("ffmpeg cannot encode opus here: %v", err)
	}
	if err := os.WriteFile(live, out[:len(out)-700], 0o600); err != nil {
		t.Fatal(err)
	}
	tool := FFmpegTool{FFmpegPath: ffmpeg, FFprobePath: ffprobe}
	if _, err := tool.Duration(context.Background(), live); err == nil {
		t.Fatal("fixture unexpectedly reports a duration")
	}
	fixed := filepath.Join(dir, "fixed.webm")
	if err := tool.Remux(context.Background(), live, fixed, "webm"); err != nil {
		t.Fatal(err)
	}
	seconds, err := tool.Duration(context.Background(), fixed)
	if err != nil || seconds < 15 || seconds > 20.5 {
		t.Fatalf("remuxed duration %v err %v", seconds, err)
	}
	data, err := os.ReadFile(fixed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte{0x1c, 0x53, 0xbb, 0x6b}) {
		t.Fatal("remuxed file has no Cues element")
	}
}
