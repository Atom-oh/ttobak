package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type newsDeleteFixture struct {
	*newsReadFixture
	before func()
	writes int
}

func (f *newsDeleteFixture) MarkNewsDeleted(_ context.Context, key repository.NewsReadKey, observed map[string]types.AttributeValue, userID string, isAdmin bool) error {
	if f.before != nil {
		f.before()
	}
	config := f.rows[repository.NewsReadKey{SourceID: key.SourceID, SortKey: "CONFIG"}]
	if !reflect.DeepEqual(f.rows[key], observed) || !isAdmin && newsString(config, "ownerId") != userID {
		return repository.ErrConditionFailed
	}
	f.writes++
	f.rows[key]["qualityStatus"] = &types.AttributeValueMemberS{Value: "DELETED"}
	return nil
}

type newsDeleteS3Fixture struct {
	*newsS3Fixture
	t       *testing.T
	state   *newsDeleteFixture
	key     repository.NewsReadKey
	deletes int
	failAt  int
}

func (f *newsDeleteS3Fixture) DeleteObject(_ context.Context, _ *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	if newsString(f.state.rows[f.key], "qualityStatus") != "DELETED" {
		f.t.Fatal("object cleanup preceded canonical revocation")
	}
	f.deletes++
	if f.deletes == f.failAt {
		return nil, errors.New("object cleanup failed")
	}
	return &s3.DeleteObjectOutput{}, nil
}

func TestNewsDeleteUnpublishesBeforeCleanupAndRetriesTombstone(t *testing.T) {
	svc, rows, object, keys := newNewsReadFixture(t)
	state := &newsDeleteFixture{newsReadFixture: rows}
	storage := &newsDeleteS3Fixture{newsS3Fixture: object, t: t, state: state, key: keys[0], failAt: 2}
	svc.news, svc.s3, svc.artifactBucketName = state, storage, "assets"
	svc.repo.(*mockCrawlerRepo).sources[keys[0].SourceID] = &model.CrawlerSource{OwnerID: "owner"}
	if err := svc.DeleteDocument(context.Background(), "owner", false, keys[0].SourceID, strings.Repeat("a", 16)); err == nil {
		t.Fatal("cleanup failure was hidden")
	}
	if state.writes != 1 || newsString(rows.rows[keys[0]], "qualityStatus") != "DELETED" {
		t.Fatal("cleanup failure left the article published")
	}
	if _, err := svc.GetDocumentDetail(context.Background(), keys[0].SourceID, strings.Repeat("a", 16)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unpublished object remained readable: %v", err)
	}
	// Configuration changes after publication do not strand cleanup.
	rows.rows[keys[1]]["status"] = &types.AttributeValueMemberS{Value: "disabled"}
	if err := svc.DeleteDocument(context.Background(), "owner", false, keys[0].SourceID, strings.Repeat("a", 16)); err != nil {
		t.Fatal(err)
	}
	if state.writes != 2 || storage.deletes != 4 || rows.rows[keys[0]] == nil {
		t.Fatal("retry removed the canonical tombstone or skipped cleanup")
	}
}

func TestNewsDeleteRejectsPublicationOrOwnerRacesBeforeCleanup(t *testing.T) {
	for _, race := range []string{"owner", "publication"} {
		t.Run(race, func(t *testing.T) {
			svc, rows, object, keys := newNewsReadFixture(t)
			state := &newsDeleteFixture{newsReadFixture: rows}
			storage := &newsDeleteS3Fixture{newsS3Fixture: object, t: t, state: state, key: keys[0]}
			svc.news, svc.s3, svc.artifactBucketName = state, storage, "assets"
			svc.repo.(*mockCrawlerRepo).sources[keys[0].SourceID] = &model.CrawlerSource{OwnerID: "owner"}
			state.before = func() {
				if race == "owner" {
					rows.rows[keys[1]]["ownerId"] = &types.AttributeValueMemberS{Value: "new-owner"}
				} else {
					rows.rows[keys[0]]["attemptToken"] = &types.AttributeValueMemberS{Value: strings.Repeat("d", 32)}
				}
			}
			err := svc.DeleteDocument(context.Background(), "owner", false, keys[0].SourceID, strings.Repeat("a", 16))
			if !errors.Is(err, repository.ErrConditionFailed) || state.writes != 0 || storage.deletes != 0 {
				t.Fatalf("racing delete was accepted: %v %d %d", err, state.writes, storage.deletes)
			}
		})
	}
}
