package model

import (
	"crypto/sha256"
	"encoding/hex"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	IndexManualKind   = "manualKbDocument"
	IndexSharedKind   = "sharedKbDocument"
	IndexManualPrefix = "manual-kb/v1/"
	IndexSharedPrefix = "shared-kb/v1/"
)

// KnowledgeIndexResource pins an existing S3 source. kb/{owner}/{filename} is
// private to that owner; shared/** retains the baseline authenticated-shared
// visibility. Neither creates a DOC# record or an unauthenticated resource.
func KnowledgeIndexResource(key string) (IndexResource, bool) {
	if !utf8.ValidString(key) || len(key) > 1024 ||
		strings.ContainsFunc(key, func(r rune) bool { return r < 32 || r == 127 || r == '\\' }) ||
		strings.HasSuffix(strings.ToLower(key), ".metadata.json") {
		return IndexResource{}, false
	}
	parts := strings.Split(key, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return IndexResource{}, false
		}
	}
	r := IndexResource{SourceKey: key}
	switch {
	case len(parts) == 3 && parts[0] == "kb" && indexID.MatchString(parts[1]):
		r.Kind, r.PK = IndexManualKind, "USER#"+parts[1]
	case len(parts) >= 2 && parts[0] == "shared":
		r.Kind, r.PK = IndexSharedKind, "KB#SHARED"
	default:
		return IndexResource{}, false
	}
	sum := sha256.Sum256([]byte(key))
	r.ID = hex.EncodeToString(sum[:])
	r.SK = "KBFILE#" + r.ID
	return r, true
}

func (r IndexResource) IsKnowledgeSource() bool {
	return r.Kind == IndexManualKind || r.Kind == IndexSharedKind
}

func (r IndexResource) Valid() bool {
	if r.IsKnowledgeSource() {
		expected, ok := KnowledgeIndexResource(r.SourceKey)
		return ok && expected == r
	}
	expected, ok := CanonicalIndexResource(r.PK, r.SK)
	return ok && expected == r
}

// Plain legacy text remains readable directly by QA. The migration covers all
// binary formats exposed by the existing manual uploader; unsupported formats
// get durable failure status rather than fabricated successful indexing.
func KnowledgeBinaryKey(key string) bool {
	switch strings.ToLower(path.Ext(key)) {
	case ".pdf", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx":
		return true
	default:
		return false
	}
}

var knowledgeHash = regexp.MustCompile(`^[0-9a-f]{64}$`)
var knowledgeRun = regexp.MustCompile(`^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$`)
var knowledgeFilename = regexp.MustCompile(`^document\.[a-z0-9]{1,16}(?:\.metadata\.json)?$`)

func KnowledgeProjectionKey(key string) bool {
	parts := strings.Split(key, "/")
	offset := 0
	switch {
	case len(parts) == 7 && parts[0] == "manual-kb" && parts[1] == "v1" && indexID.MatchString(parts[2]):
		offset = 3
	case len(parts) == 6 && parts[0] == "shared-kb" && parts[1] == "v1":
		offset = 2
	default:
		return false
	}
	return knowledgeHash.MatchString(parts[offset]) && knowledgeHash.MatchString(parts[offset+1]) &&
		knowledgeRun.MatchString(parts[offset+2]) && knowledgeFilename.MatchString(parts[offset+3])
}

func KnowledgeProjectionPrefix(prefix string) bool {
	if !strings.HasSuffix(prefix, "/") {
		return false
	}
	parts := strings.Split(strings.TrimSuffix(prefix, "/"), "/")
	return (len(parts) == 4 && parts[0] == "manual-kb" && parts[1] == "v1" &&
		indexID.MatchString(parts[2]) && knowledgeHash.MatchString(parts[3])) ||
		(len(parts) == 3 && parts[0] == "shared-kb" && parts[1] == "v1" && knowledgeHash.MatchString(parts[2]))
}
