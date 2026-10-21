package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type newsReadFixture struct {
	rows  map[repository.NewsReadKey]map[string]types.AttributeValue
	reads int
	fail  error
	after func()
}

func (f *newsReadFixture) ReadNewsItems(_ context.Context, keys []repository.NewsReadKey) (map[repository.NewsReadKey]map[string]types.AttributeValue, error) {
	f.reads++
	if f.fail != nil {
		return nil, f.fail
	}
	result := map[repository.NewsReadKey]map[string]types.AttributeValue{}
	for _, key := range keys {
		if f.rows[key] == nil {
			continue
		}
		result[key] = map[string]types.AttributeValue{}
		for name, value := range f.rows[key] {
			result[key][name] = value
		}
	}
	if f.after != nil {
		f.after()
	}
	return result, nil
}

type newsS3Fixture struct {
	mockS3GetDeleter
	data   string
	gets   []string
	after  func()
	getErr error
}

func (f *newsS3Fixture) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	f.gets = append(f.gets, aws.ToString(in.Key))
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.after != nil {
		f.after()
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader(f.data))}, nil
}

func newsAV(t *testing.T, values map[string]any) map[string]types.AttributeValue {
	t.Helper()
	row, err := attributevalue.MarshalMap(values)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func legacyNewsRows(t *testing.T, sourceID string, docs []model.CrawledDocument) *newsReadFixture {
	t.Helper()
	fixture := &newsReadFixture{rows: map[repository.NewsReadKey]map[string]types.AttributeValue{}}
	fixture.rows[repository.NewsReadKey{SourceID: sourceID, SortKey: "CONFIG"}] = newsAV(t, map[string]any{
		"PK": "CRAWLER#" + sourceID, "SK": "CONFIG", "sourceId": sourceID, "status": "active",
	})
	for _, doc := range docs {
		row, err := attributevalue.MarshalMap(doc)
		if err != nil {
			t.Fatal(err)
		}
		row["PK"] = &types.AttributeValueMemberS{Value: "CRAWLER#" + sourceID}
		row["SK"] = &types.AttributeValueMemberS{Value: "DOC#" + doc.DocHash}
		fixture.rows[repository.NewsReadKey{SourceID: sourceID, SortKey: "DOC#" + doc.DocHash}] = row
	}
	return fixture
}

func newNewsReadFixture(t *testing.T) (*InsightsService, *newsReadFixture, *newsS3Fixture, []repository.NewsReadKey) {
	t.Helper()
	keys := newsKeys("우리은행", strings.Repeat("a", 16))
	config := newsAV(t, map[string]any{
		"PK": "CRAWLER#우리은행", "SK": "CONFIG", "sourceId": "우리은행",
		"sourceName": "우리은행", "ownerId": "owner", "status": "idle",
	})
	revision, err := newsSourceRevision(config)
	if err != nil {
		t.Fatal(err)
	}
	object := &newsS3Fixture{data: "# Current published news\n\nEvidence."}
	sum := sha256.Sum256([]byte(object.data))
	key := "news/v1/우리은행/" + strings.Repeat("a", 16) + "/" + strings.Repeat("b", 32)
	row := newsAV(t, map[string]any{
		"PK": "CRAWLER#우리은행", "SK": "DOC#" + strings.Repeat("a", 16),
		"sourceId": "우리은행", "docHash": strings.Repeat("a", 16),
		"schemaVersion": 1, "qualityStatus": "PUBLISHED", "type": "news",
		"attemptToken": strings.Repeat("b", 32), "sourceRevision": revision,
		"s3Key": key + ".md", "artifactKey": "knowledge-artifacts/" + key + ".json",
		"markdownSha256": hex.EncodeToString(sum[:]), "artifactSha256": strings.Repeat("c", 64),
		"title": "Current publication", "crawledAt": 1,
	})
	fixture := &newsReadFixture{rows: map[repository.NewsReadKey]map[string]types.AttributeValue{
		keys[0]: row, keys[1]: config,
	}}
	repo := newMockCrawlerRepo()
	repo.documents["우리은행"] = []model.CrawledDocument{{
		SourceID: "우리은행", DocHash: strings.Repeat("a", 16), Type: "news",
		Title: "Stale cached title", S3Key: "shared/news/우리은행/" + strings.Repeat("a", 16) + ".md",
	}}
	return &InsightsService{repo: repo, news: fixture, s3: object, kbBucketName: "knowledge"}, fixture, object, keys
}

func TestNewsSourceRevisionMatchesCrawlerVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/news-source-revisions.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Source   map[string]any `json:"source"`
		Revision string         `json:"revision"`
	}
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	for i, vector := range vectors {
		got, err := newsSourceRevision(newsAV(t, vector.Source))
		if err != nil || got != vector.Revision {
			t.Fatalf("vector %d: revision %s, want %s, err %v", i, got, vector.Revision, err)
		}
	}
}

func TestPublishedNewsTitleMatchesProducerWhitespaceContract(t *testing.T) {
	for _, title := range []string{"first\nsecond\r\nthird\tpart", "  " + strings.Repeat("가", 300) + "  ", "title" + string(rune(127)),
		strings.Repeat(" ", 25000) + "Accepted title"} {
		svc, fixture, _, keys := newNewsReadFixture(t)
		fixture.rows[keys[0]]["title"] = &types.AttributeValueMemberS{Value: title}
		got, err := svc.GetDocumentDetail(context.Background(), keys[0].SourceID, strings.Repeat("a", 16))
		if err != nil || got.Title != strings.TrimSpace(title) {
			t.Fatalf("producer-valid title rejected or changed: %q %v", title, err)
		}
		list, err := svc.currentNewsList(context.Background(), svc.repo.(*mockCrawlerRepo).documents[keys[0].SourceID])
		if err != nil || len(list) != 1 || list[0].Title != strings.TrimSpace(title) {
			t.Fatalf("producer-valid padding broke list: %v", err)
		}
		if newsString(fixture.rows[keys[0]], "title") != title {
			t.Fatal("display projection changed canonical title")
		}
	}
	for _, title := range []string{" ", "title\x01", strings.Repeat("가", 301)} {
		if validNewsTitle(&types.AttributeValueMemberS{Value: title}) {
			t.Fatal("invalid producer title accepted")
		}
	}
}

func TestLegacyTechDetailKeepsReservedPartition(t *testing.T) {
	repo := newMockCrawlerRepo()
	repo.documents["__tech__"] = []model.CrawledDocument{{
		SourceID: "__tech__", DocHash: "techhash", Type: "tech",
		S3Key: "shared/aws-docs/lambda/techhash.md", Title: "Lambda",
	}}
	object := &newsS3Fixture{data: "# Lambda"}
	svc := &InsightsService{repo: repo, s3: object, kbBucketName: "knowledge"}
	if _, err := svc.GetDocumentDetail(context.Background(), "__tech__", "techhash"); err != nil {
		t.Fatalf("legacy tech article no longer opens: %v", err)
	}
	if len(object.gets) != 1 || object.gets[0] != "shared/aws-docs/lambda/techhash.md" {
		t.Fatal("tech detail used the wrong stored key")
	}
	for _, sourceID := range []string{"__other__", "../tech", "__tech__/other"} {
		if _, err := svc.GetDocumentDetail(context.Background(), sourceID, "techhash"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("unexpected reserved partition permitted: %q %v", sourceID, err)
		}
	}
	repo.documents["__tech__"][0].Type = "news"
	if _, err := svc.GetDocumentDetail(context.Background(), "__tech__", "techhash"); !errors.Is(err, ErrNotFound) {
		t.Fatal("tech exception admitted reserved news source")
	}
}

func TestLegacyNewsRemainsReadableAfterSourceDisabled(t *testing.T) {
	doc := model.CrawledDocument{SourceID: "legacy", DocHash: "oldhash", Type: "news",
		Title: "Retained legacy news", S3Key: "shared/news/legacy/oldhash.md"}
	repo := newMockCrawlerRepo()
	repo.documents["legacy"] = []model.CrawledDocument{doc}
	fixture := legacyNewsRows(t, "legacy", []model.CrawledDocument{doc})
	fixture.rows[repository.NewsReadKey{SourceID: "legacy", SortKey: "CONFIG"}]["status"] = &types.AttributeValueMemberS{Value: "disabled"}
	svc := &InsightsService{repo: repo, news: fixture, s3: &newsS3Fixture{data: "# Retained legacy news"}, kbBucketName: "knowledge"}
	list, err := svc.currentNewsList(context.Background(), []model.CrawledDocument{doc})
	if err != nil || len(list) != 1 {
		t.Fatalf("disabled legacy source hid existing news: %v", err)
	}
	if _, err := svc.GetDocumentDetail(context.Background(), "legacy", "oldhash"); err != nil {
		t.Fatalf("disabled legacy source prevented detail: %v", err)
	}
}

func TestPublishedNewsReadsCanonicalObjectAndRejectsChangedState(t *testing.T) {
	for _, mutation := range []string{"none", "disabled", "owner", "unpublished", "title", "missing"} {
		t.Run(mutation, func(t *testing.T) {
			svc, fixture, object, keys := newNewsReadFixture(t)
			object.after = func() {
				switch mutation {
				case "disabled":
					fixture.rows[keys[1]]["status"] = &types.AttributeValueMemberS{Value: "disabled"}
				case "owner":
					fixture.rows[keys[1]]["ownerId"] = &types.AttributeValueMemberS{Value: "new-owner"}
				case "unpublished":
					fixture.rows[keys[0]]["qualityStatus"] = &types.AttributeValueMemberS{Value: "DELETED"}
				case "title":
					fixture.rows[keys[0]]["title"] = &types.AttributeValueMemberS{Value: "Replacement"}
				case "missing":
					delete(fixture.rows, keys[0])
				}
			}
			got, err := svc.GetDocumentDetail(context.Background(), keys[0].SourceID, strings.Repeat("a", 16))
			if mutation == "none" {
				if err != nil || got.Content != object.data || got.Title != "Current publication" {
					t.Fatalf("unexpected detail: %+v, %v", got, err)
				}
			} else if !errors.Is(err, ErrNotFound) || got != nil {
				t.Fatalf("changed state returned content: %+v, %v", got, err)
			}
			if len(object.gets) != 1 || !strings.HasPrefix(object.gets[0], "news/v1/") {
				t.Fatalf("unexpected object reads: %v", object.gets)
			}
		})
	}
}

func TestPublishedNewsRejectsInvalidPublicationBeforeObjectRead(t *testing.T) {
	mutations := map[string]types.AttributeValue{
		"schemaVersion":  &types.AttributeValueMemberBOOL{Value: true},
		"qualityStatus":  &types.AttributeValueMemberS{Value: "FAILED"},
		"sourceId":       &types.AttributeValueMemberS{Value: "another"},
		"type":           &types.AttributeValueMemberS{Value: "blog"},
		"attemptToken":   &types.AttributeValueMemberS{Value: strings.Repeat("z", 32)},
		"artifactKey":    &types.AttributeValueMemberS{Value: "private/secret.json"},
		"s3Key":          &types.AttributeValueMemberS{Value: "news/v1/other.md"},
		"sourceRevision": &types.AttributeValueMemberS{Value: strings.Repeat("e", 64)},
	}
	for field, value := range mutations {
		t.Run(field, func(t *testing.T) {
			svc, fixture, object, keys := newNewsReadFixture(t)
			fixture.rows[keys[0]][field] = value
			got, err := svc.GetDocumentDetail(context.Background(), keys[0].SourceID, strings.Repeat("a", 16))
			if !errors.Is(err, ErrNotFound) || got != nil || len(object.gets) != 0 {
				t.Fatalf("invalid publication read object: %+v %v %v", got, err, object.gets)
			}
		})
	}
}

func TestPublishedNewsRejectsChangedOrOversizedBytesWithoutFallback(t *testing.T) {
	for _, data := range []string{"", "wrong", strings.Repeat("a", maxPublishedNewsBytes+1), string([]byte{255})} {
		svc, _, object, keys := newNewsReadFixture(t)
		object.data = data
		got, err := svc.GetDocumentDetail(context.Background(), keys[0].SourceID, strings.Repeat("a", 16))
		if !errors.Is(err, ErrNotFound) || got != nil || len(object.gets) != 1 {
			t.Fatalf("invalid bytes accepted or retried: %v %v", err, object.gets)
		}
	}
	svc, _, object, keys := newNewsReadFixture(t)
	object.getErr = errors.New("object unavailable")
	if _, err := svc.GetDocumentDetail(context.Background(), keys[0].SourceID, strings.Repeat("a", 16)); err == nil || len(object.gets) != 1 {
		t.Fatalf("object failure must not fall back: %v %v", err, object.gets)
	}
}

func TestNewsListRevalidatesCacheBeforePagination(t *testing.T) {
	scanCache.clear()
	defer scanCache.clear()
	svc, fixture, _, keys := newNewsReadFixture(t)
	stale := model.CrawledDocument{SourceID: keys[0].SourceID, DocHash: strings.Repeat("a", 16), Type: "news", Title: "Stale"}
	scanCache.set("scan:news", []model.CrawledDocument{stale}, 1)
	result, err := svc.ListInsights(context.Background(), "news", "", "", nil, "", 1, 20)
	if err != nil || len(result.Documents) != 1 || result.Documents[0].Title != "Current publication" {
		t.Fatalf("cache did not hydrate canonical state: %+v, %v", result, err)
	}
	fixture.rows[keys[0]]["qualityStatus"] = &types.AttributeValueMemberS{Value: "DELETED"}
	result, err = svc.ListInsights(context.Background(), "news", "", "", nil, "", 1, 20)
	if err != nil || len(result.Documents) != 0 || result.TotalCount != 0 {
		t.Fatalf("cached deletion was visible: %+v, %v", result, err)
	}
	fixture.fail = errors.New("incomplete current state")
	if _, err := svc.ListInsights(context.Background(), "news", "", "", nil, "", 1, 20); err == nil {
		t.Fatal("failed current read silently used cache")
	}
}

func TestMissingMetadataNeverReadsOrphanObject(t *testing.T) {
	svc, _, object, _ := newNewsReadFixture(t)
	if _, err := svc.GetDocumentDetail(context.Background(), "missing", "document"); !errors.Is(err, ErrNotFound) || len(object.gets) != 0 {
		t.Fatalf("orphan fallback: %v %v", err, object.gets)
	}
}
