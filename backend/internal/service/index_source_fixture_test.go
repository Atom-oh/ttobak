package service

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/ttobak/backend/internal/model"
)

type indexSourceRepo struct{ sources map[string]*model.IndexRecord }

func (r *indexSourceRepo) GetIndexSource(_ context.Context, key model.IndexResource) (*model.IndexRecord, error) {
	record := r.sources[key.Hash()]
	if record == nil {
		return nil, nil
	}
	result := &model.IndexRecord{Resource: record.Resource, Fields: map[string]interface{}{}}
	for field, value := range record.Fields {
		result.Fields[field] = value
	}
	return result, nil
}

type indexSourceObjects struct {
	assets     map[string][]byte
	heads      map[string]IndexObject
	beforeRead func()
}

func (p *indexSourceObjects) asset(key, body, version string) {
	p.assets[key] = []byte(body)
	p.heads[key] = IndexObject{Key: key, ETag: fmt.Sprintf(`"%x"`, sha256.Sum256([]byte(body))),
		VersionID: version, Size: int64(len(body))}
}
func (p *indexSourceObjects) Head(_ context.Context, key string) (IndexObject, error) {
	object, ok := p.heads[key]
	if !ok {
		return IndexObject{}, ErrIndexMissing
	}
	return object, nil
}
func (p *indexSourceObjects) Read(_ context.Context, object IndexObject) ([]byte, error) {
	if p.beforeRead != nil {
		before := p.beforeRead
		p.beforeRead = nil
		before()
	}
	current, ok := p.heads[object.Key]
	if !ok {
		return nil, ErrIndexMissing
	}
	if current.ETag != object.ETag || current.VersionID != object.VersionID {
		return nil, ErrIndexChanged
	}
	return append([]byte(nil), p.assets[object.Key]...), nil
}

func newIndexSourceTest() (*IndexSourceReader, *indexSourceRepo, *indexSourceObjects) {
	repo := &indexSourceRepo{sources: map[string]*model.IndexRecord{}}
	objects := &indexSourceObjects{assets: map[string][]byte{}, heads: map[string]IndexObject{}}
	return NewIndexSourceReader(repo, objects, "assets"), repo, objects
}

func addIndexSourceMeeting(repo *indexSourceRepo, id, notes string) model.IndexResource {
	key, _ := model.CanonicalIndexResource("USER#owner", "MEETING#"+id)
	repo.sources[key.Hash()] = &model.IndexRecord{Resource: key, Fields: map[string]interface{}{
		"meetingId": id, "userId": "owner", "title": "Title", "notes": notes, "content": "saved summary",
		"transcriptA": "unselected A", "transcriptB": "selected B", "selectedTranscript": "B",
		"actionItems": `[{"text":"task","completed":true}]`, "updatedAt": "fixed-time",
	}}
	return key
}
