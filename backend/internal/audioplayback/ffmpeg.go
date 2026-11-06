package audioplayback

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// FFmpegTool runs ffmpeg/ffprobe with a file-only protocol whitelist and an
// environment stripped of AWS credentials.
type FFmpegTool struct {
	FFmpegPath, FFprobePath string
}

func (t FFmpegTool) Remux(ctx context.Context, inPath, outPath, format string) error {
	if format != "webm" && format != "ogg" {
		return fmt.Errorf("unsupported format %q", format)
	}
	// -c copy rewrites the container only. The muxer writes Duration and Cues
	// for a seekable file output, and a truncated final cluster is dropped.
	_, err := t.run(ctx, t.FFmpegPath, RemuxArgs(inPath, outPath, format)...)
	return err
}

func (t FFmpegTool) Duration(ctx context.Context, filePath string) (float64, error) {
	out, err := t.run(ctx, t.FFprobePath,
		"-v", "error", "-protocol_whitelist", "file",
		"-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", "file:"+filePath)
	if err != nil {
		return 0, err
	}
	return ParseProbeDuration(out)
}

// RemuxArgs is exported for tests; it never lets the input select a protocol
// other than a local file.
func RemuxArgs(inPath, outPath, format string) []string {
	return []string{
		"-nostdin", "-hide_banner", "-loglevel", "error",
		"-protocol_whitelist", "file",
		"-i", "file:" + inPath,
		"-map", "0:a", "-c", "copy",
		"-f", format, "-y", "file:" + outPath,
	}
}

// ParseProbeDuration reads ffprobe's bare `format=duration` output.
func ParseProbeDuration(out []byte) (float64, error) {
	text := strings.TrimSpace(string(out))
	seconds, err := strconv.ParseFloat(text, 64)
	if err != nil || seconds != seconds || seconds <= 0 || seconds > 1e7 {
		return 0, fmt.Errorf("invalid probe duration %q", truncate(text, 64))
	}
	return seconds, nil
}

func (t FFmpegTool) run(ctx context.Context, binary string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = sanitizedEnv(os.Environ())
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s failed: %w (%s)", binary, err, truncate(stderr.String(), 1000))
	}
	return stdout.Bytes(), nil
}

func sanitizedEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if !strings.HasPrefix(kv, "AWS_") {
			out = append(out, kv)
		}
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}
