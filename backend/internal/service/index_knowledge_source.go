package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/ttobak/backend/internal/model"
)

func knowledgeSchema(key model.IndexResource) string {
	if key.Kind == model.IndexManualKind {
		return "manual-kb-v1"
	}
	return "shared-kb-v1"
}

// KnowledgeSourceRevision is shared with QA: UTF-8 byte-length framed strings.
// Missing objects use empty ETag/version and size zero, distinct from empty files.
func KnowledgeSourceRevision(schema, bucket, key string, object IndexObject) string {
	hash := sha256.New()
	for _, value := range []string{schema, bucket, key, object.ETag, object.VersionID, strconv.FormatInt(object.Size, 10)} {
		fmt.Fprintf(hash, "%d:", len([]byte(value)))
		hash.Write([]byte(value))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func (s *IndexingService) readKnowledgeSource(ctx context.Context, key model.IndexResource, bodies bool) (*IndexSnapshot, error) {
	bucket := s.objects.KnowledgeBucket()
	if bucket == "" {
		return nil, ErrIndexInvalid
	}
	object, err := s.objects.HeadKnowledge(ctx, key.SourceKey)
	if errors.Is(err, ErrIndexMissing) {
		object, err = IndexObject{Key: key.SourceKey, Missing: true}, nil
	}
	if err != nil {
		return nil, err
	}
	if object.Key != key.SourceKey || object.Size < 0 || (!object.Missing && object.ETag == "") {
		return nil, ErrIndexInvalid
	}
	snapshot := &IndexSnapshot{KnowledgeBucket: bucket, Outcome: model.IndexIndexed,
		Objects: []IndexObject{object}, Revision: KnowledgeSourceRevision(knowledgeSchema(key), bucket, key.SourceKey, object)}
	if object.Missing {
		snapshot.Outcome = model.IndexDeleted
		return snapshot, nil
	}
	extension := strings.ToLower(path.Ext(key.SourceKey))
	types := map[string]string{
		".pdf": "application/pdf", ".doc": "application/msword",
		".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		".xls":  "application/vnd.ms-excel",
		".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	}
	contentType, supported := types[extension]
	switch {
	case !supported:
		snapshot.Outcome, snapshot.ErrorCode = model.IndexFailed, "UNSUPPORTED_FILE"
	case object.Size == 0:
		snapshot.Outcome, snapshot.ErrorCode = model.IndexFailed, "EMPTY_FILE"
	case object.Size > MaxIndexObjectBytes:
		snapshot.Outcome, snapshot.ErrorCode = model.IndexFailed, "SOURCE_TOO_LARGE"
	}
	if snapshot.Outcome == model.IndexFailed {
		return snapshot, nil
	}
	if object.ContentType != "" {
		contentType = object.ContentType
	}
	if bodies {
		body, err := s.objects.ReadKnowledge(ctx, object)
		if err != nil {
			if errors.Is(err, ErrIndexChanged) {
				snapshot.ErrorCode = "SOURCE_CHANGED"
			}
			return snapshot, err
		}
		if int64(len(body)) != object.Size {
			return snapshot, ErrIndexChanged
		}
		// A pinned old version is readable after overwrite. Verify that it is
		// still the current original before publishing its immutable copy.
		current, err := s.objects.HeadKnowledge(ctx, key.SourceKey)
		if err != nil {
			return snapshot, err
		}
		if KnowledgeSourceRevision(knowledgeSchema(key), bucket, key.SourceKey, current) != snapshot.Revision ||
			current.ContentType != object.ContentType {
			snapshot.ErrorCode = "SOURCE_CHANGED"
			return snapshot, ErrIndexChanged
		}
		snapshot.Parts = []IndexPart{{Name: "document" + extension, ContentType: contentType, Body: body}}
	}
	if _, err := knowledgeMetadata(key, snapshot, "00000000-0000-0000-0000-000000000000"); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

func knowledgeMetadata(key model.IndexResource, source *IndexSnapshot, run string) ([]byte, error) {
	if !key.Valid() || len(source.Objects) != 1 || source.KnowledgeBucket == "" {
		return nil, ErrIndexInvalid
	}
	object := source.Objects[0]
	attributes := map[string]interface{}{
		"indexSchema": knowledgeSchema(key), "resourceKind": key.Kind, "resourceId": key.ID,
		"sourceBucket": source.KnowledgeBucket, "sourceKey": key.SourceKey,
		"sourceETag": object.ETag, "sourceVersionId": object.VersionID, "sourceSize": object.Size,
		"sourceRevision": source.Revision, "indexRunId": run,
	}
	if key.Kind == model.IndexManualKind {
		attributes["ownerId"] = strings.TrimPrefix(key.PK, "USER#")
	} else {
		attributes["visibility"] = "authenticated-shared"
	}
	data, err := json.Marshal(map[string]interface{}{"metadataAttributes": attributes})
	if len(data) > 10000 {
		return nil, ErrIndexInvalid
	}
	return data, err
}

func indexRunPrefix(key model.IndexResource, revision, run string) string {
	if key.IsKnowledgeSource() {
		return key.Prefix() + revision + "/" + run + "/"
	}
	return key.Prefix() + run + "/"
}

func (s *IndexingService) reconcileKnowledge(ctx context.Context, control *model.IndexControl) error {
	for _, scan := range []struct {
		prefix string
		cursor *string
	}{{"kb/", &control.ManualCursor}, {"shared/", &control.SharedCursor}} {
		keys, next, err := s.objects.KnowledgePage(ctx, scan.prefix, *scan.cursor)
		if err != nil {
			return err
		}
		for _, objectKey := range keys {
			key, ok := model.KnowledgeIndexResource(objectKey)
			if !ok || !model.KnowledgeBinaryKey(objectKey) {
				continue
			}
			revision := ""
			if source, err := s.ReadSource(ctx, key, false); err == nil {
				revision = source.Revision
			} else if !permanentIndexSourceError(err) {
				if err := s.queueUnreadable(ctx, key); err != nil {
					return err
				}
				continue
			}
			// Unknown revisions retain PR205's failure cooldown; a catalog
			// notification cannot erase backoff without evidence of new bytes.
			if err := s.repo.RequestIndexResource(ctx, key, revision, s.now().UnixMilli()); err != nil {
				return err
			}
		}
		*scan.cursor = next
	}
	return nil
}
