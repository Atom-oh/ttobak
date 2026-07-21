// Package convertdoc holds cmd/convert-doc's pure logic, extracted so it's
// unit-testable under go test ./internal/... without pulling in that
// command's init() (AWS config load + BUCKET_NAME fail-fast, unsuitable to
// run in a test binary).
package convertdoc

import (
	"path/filepath"
	"strings"
)

// SanitizedEnv returns env with every AWS_* variable removed. Lambda
// injects temporary credentials (AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY/
// AWS_SESSION_TOKEN, plus AWS_CONTAINER_CREDENTIALS_* etc.) as env vars for
// the Go SDK to pick up automatically -- the soffice subprocess this feeds
// has no legitimate need for any of them.
func SanitizedEnv(env []string) []string {
	filtered := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "AWS_") {
			continue
		}
		filtered = append(filtered, kv)
	}
	return filtered
}

// TruncateOutput caps out at n bytes for safe inclusion in an error/log
// message -- soffice's stdout/stderr on a malformed/hostile input can echo
// fragments of the document (paths, embedded object names), so this bounds
// what ends up in CloudWatch via the Lambda runtime's own error logging.
func TruncateOutput(out []byte, n int) string {
	if len(out) <= n {
		return string(out)
	}
	return string(out[:n]) + "...(truncated)"
}

// IsSlideExtension reports whether key's extension is .ppt or .pptx,
// case-insensitively.
func IsSlideExtension(key string) bool {
	ext := strings.ToLower(filepath.Ext(key))
	return ext == ".ppt" || ext == ".pptx"
}
