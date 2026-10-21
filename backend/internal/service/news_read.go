package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type newsStateReader interface {
	ReadNewsItems(context.Context, []repository.NewsReadKey) (map[repository.NewsReadKey]map[string]types.AttributeValue, error)
}

var newsHash = regexp.MustCompile(`^[0-9a-f]{16}$`)
var newsToken = regexp.MustCompile(`^[0-9a-f]{32}$`)
var newsDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

const maxPublishedNewsBytes = 64000

func validNewsSourceID(value string) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) < 1 ||
		utf8.RuneCountInString(value) > 128 || strings.HasPrefix(value, "__") {
		return false
	}
	for _, char := range value {
		if !unicode.IsLetter(char) && !unicode.IsDigit(char) && char != '_' && char != '-' {
			return false
		}
	}
	return true
}

func newsString(row map[string]types.AttributeValue, field string) string {
	value, ok := row[field].(*types.AttributeValueMemberS)
	if !ok {
		return ""
	}
	return value.Value
}

func newsText(value types.AttributeValue, fallback string, maximum int, empty bool) (string, error) {
	text := fallback
	if value != nil {
		item, ok := value.(*types.AttributeValueMemberS)
		if !ok {
			return "", ErrNotFound
		}
		text = item.Value
	}
	if !utf8.ValidString(text) || utf8.RuneCountInString(text) > maximum {
		return "", ErrNotFound
	}
	for _, char := range text {
		if char < 32 || char == 127 {
			return "", ErrNotFound
		}
	}
	text = strings.TrimSpace(text)
	if !empty && text == "" {
		return "", ErrNotFound
	}
	return text, nil
}

// Python's staged contract treats false-valued legacy optional lists as empty.
// Preserve that fingerprint exactly; populated values still require a list.
func emptyNewsValue(value types.AttributeValue) bool {
	switch item := value.(type) {
	case nil:
		return true
	case *types.AttributeValueMemberNULL:
		return item.Value
	case *types.AttributeValueMemberBOOL:
		return !item.Value
	case *types.AttributeValueMemberS:
		return item.Value == ""
	case *types.AttributeValueMemberN:
		number, ok := new(big.Rat).SetString(item.Value)
		return ok && number.Sign() == 0
	case *types.AttributeValueMemberM:
		return len(item.Value) == 0
	}
	return false
}

func newsTerms(value types.AttributeValue, maximum, textLimit int, deduplicate bool) ([]string, error) {
	result := []string{}
	if emptyNewsValue(value) {
		return result, nil
	}
	list, ok := value.(*types.AttributeValueMemberL)
	if !ok || len(list.Value) > maximum {
		return nil, ErrNotFound
	}
	seen := map[string]bool{}
	for _, item := range list.Value {
		text, err := newsText(item, "", textLimit, false)
		if err != nil {
			return nil, err
		}
		if !deduplicate || !seen[text] {
			result = append(result, text)
			seen[text] = true
		}
	}
	return result, nil
}

// newsSourceRevision matches Python's compact, sorted, literal UTF-8 JSON,
// including U+2028/U+2029. Do not replace escaped literal backslash sequences.
func newsSourceRevision(row map[string]types.AttributeValue) (string, error) {
	sourceID := newsString(row, "sourceId")
	if !validNewsSourceID(sourceID) {
		return "", ErrNotFound
	}
	fields := map[string]any{"sourceId": sourceID}
	for _, spec := range []struct {
		name, fallback string
		limit          int
		empty          bool
	}{{"sourceName", "", 200, true}, {"ownerId", "", 128, true}, {"status", "active", 32, false}} {
		text, err := newsText(row[spec.name], spec.fallback, spec.limit, spec.empty)
		if err != nil {
			return "", err
		}
		fields[spec.name] = text
	}
	for _, name := range []string{"aliases", "newsQueries", "customUrls"} {
		count, limit, deduplicate := 32, 200, true
		if name == "customUrls" {
			count, limit, deduplicate = 20, 4096, false
		}
		values, err := newsTerms(row[name], count, limit, deduplicate)
		if err != nil {
			return "", err
		}
		fields[name] = values
	}
	if len(fields["aliases"].([]string)) == 32 && fields["sourceName"] != "" {
		return "", ErrNotFound
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(fields); err != nil {
		return "", err
	}
	data := bytes.TrimSuffix(encoded.Bytes(), []byte{'\n'})
	var literal bytes.Buffer
	for i := 0; i < len(data); i++ {
		if data[i] == '\\' && i+1 < len(data) {
			if i+6 <= len(data) && (string(data[i:i+6]) == `\u2028` || string(data[i:i+6]) == `\u2029`) {
				literal.WriteRune(rune(0x2028 + int(data[i+5]) - '8'))
				i += 5
				continue
			}
			literal.Write(data[i : i+2])
			i++
			continue
		}
		literal.WriteByte(data[i])
	}
	sum := sha256.Sum256(literal.Bytes())
	return hex.EncodeToString(sum[:]), nil
}

func isVersionedNews(row map[string]types.AttributeValue) bool {
	return row["schemaVersion"] != nil || row["qualityStatus"] != nil ||
		row["attemptToken"] != nil || strings.HasPrefix(newsString(row, "s3Key"), "news/")
}

func validLegacyInsightKey(key, sourceID, docHash string) bool {
	if key == "shared/news/"+sourceID+"/"+docHash+".md" {
		return true
	}
	parts := strings.Split(key, "/")
	return len(parts) == 4 && parts[0] == "shared" && parts[1] == "aws-docs" &&
		validNewsSourceID(parts[2]) && parts[3] == docHash+".md"
}

func validNewsPublication(row map[string]types.AttributeValue, sourceID, docHash string) bool {
	version, ok := row["schemaVersion"].(*types.AttributeValueMemberN)
	if !ok {
		return false
	}
	number, ok := new(big.Rat).SetString(version.Value)
	if !ok || number.Cmp(big.NewRat(1, 1)) != 0 || !validNewsSourceID(sourceID) || !newsHash.MatchString(docHash) {
		return false
	}
	token := newsString(row, "attemptToken")
	if !newsToken.MatchString(token) {
		return false
	}
	key := "news/v1/" + sourceID + "/" + docHash + "/" + token
	expected := map[string]string{
		"PK": "CRAWLER#" + sourceID, "SK": "DOC#" + docHash,
		"sourceId": sourceID, "docHash": docHash, "qualityStatus": "PUBLISHED", "type": "news",
		"s3Key": key + ".md", "artifactKey": "knowledge-artifacts/" + key + ".json",
	}
	for field, value := range expected {
		if newsString(row, field) != value {
			return false
		}
	}
	for _, field := range []string{"sourceRevision", "markdownSha256", "artifactSha256"} {
		if !newsDigest.MatchString(newsString(row, field)) {
			return false
		}
	}
	if !validNewsTitle(row["title"]) {
		return false
	}
	return true
}

// Match news_enrichment._string's title contract without changing the stricter
// source-fingerprint text contract: padded titles and line whitespace are valid.
func validNewsTitle(value types.AttributeValue) bool {
	title, ok := value.(*types.AttributeValueMemberS)
	if !ok || !utf8.ValidString(title.Value) {
		return false
	}
	length := utf8.RuneCountInString(strings.TrimSpace(title.Value))
	if length < 1 || length > 300 {
		return false
	}
	for _, char := range title.Value {
		if char < 32 && char != '\n' && char != '\r' && char != '\t' {
			return false
		}
	}
	return true
}

func currentNewsPublication(row, config map[string]types.AttributeValue, sourceID, docHash string) bool {
	if !validNewsPublication(row, sourceID, docHash) {
		return false
	}
	if newsString(config, "PK") != "CRAWLER#"+sourceID || newsString(config, "SK") != "CONFIG" ||
		newsString(config, "sourceId") != sourceID || newsString(config, "status") == "disabled" {
		return false
	}
	revision, err := newsSourceRevision(config)
	return err == nil && revision == newsString(row, "sourceRevision")
}

func (s *InsightsService) newsRows(ctx context.Context, keys []repository.NewsReadKey) (map[repository.NewsReadKey]map[string]types.AttributeValue, error) {
	if s.news == nil {
		return nil, fmt.Errorf("current news reader is unavailable")
	}
	return s.news.ReadNewsItems(ctx, keys)
}

func newsKeys(sourceID, docHash string) []repository.NewsReadKey {
	return []repository.NewsReadKey{{SourceID: sourceID, SortKey: "DOC#" + docHash}, {SourceID: sourceID, SortKey: "CONFIG"}}
}

func (s *InsightsService) publishedNewsDetail(ctx context.Context, sourceID, docHash string) (*model.InsightDetailResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	keys := newsKeys(sourceID, docHash)
	rows, err := s.newsRows(ctx, keys)
	if err != nil {
		return nil, err
	}
	row := rows[keys[0]]
	if !currentNewsPublication(row, rows[keys[1]], sourceID, docHash) {
		return nil, ErrNotFound
	}
	object, err := s.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.kbBucketName), Key: aws.String(newsString(row, "s3Key")),
	})
	if err != nil {
		return nil, fmt.Errorf("read published news object: %w", err)
	}
	if object == nil || object.Body == nil {
		return nil, ErrNotFound
	}
	defer object.Body.Close()
	data, err := io.ReadAll(io.LimitReader(object.Body, maxPublishedNewsBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read published news bytes: %w", err)
	}
	sum := sha256.Sum256(data)
	if len(data) == 0 || len(data) > maxPublishedNewsBytes || !utf8.Valid(data) ||
		hex.EncodeToString(sum[:]) != newsString(row, "markdownSha256") {
		return nil, ErrNotFound
	}
	current, err := s.newsRows(ctx, keys)
	if err != nil {
		return nil, err
	}
	if !currentNewsPublication(current[keys[0]], current[keys[1]], sourceID, docHash) ||
		!reflect.DeepEqual(row, current[keys[0]]) {
		return nil, ErrNotFound
	}
	document, err := decodeNewsDocument(row, sourceID, docHash)
	if err != nil {
		return nil, err
	}
	return &model.InsightDetailResponse{CrawledDocument: document, Content: string(data)}, nil
}

func decodeNewsDocument(row map[string]types.AttributeValue, sourceID, docHash string) (model.CrawledDocument, error) {
	var document model.CrawledDocument
	if err := attributevalue.UnmarshalMap(row, &document); err != nil {
		return document, fmt.Errorf("decode current news metadata: %w", err)
	}
	document.SourceID, document.DocHash = sourceID, docHash
	texts := []string{document.Title, document.URL, document.Source, document.Summary, document.PubDate}
	texts = append(texts, document.Tags...)
	texts = append(texts, document.AWSServices...)
	for _, value := range texts {
		if !utf8.ValidString(value) {
			return model.CrawledDocument{}, ErrNotFound
		}
	}
	encoded, err := json.Marshal(document)
	if err != nil || len(encoded) > 24000 {
		return model.CrawledDocument{}, fmt.Errorf("news metadata exceeds response budget")
	}
	return document, nil
}

// Cached/GSI records only discover identities. Re-read current news rows before
// filtering and pagination, including when an earlier legacy row was replaced.
func (s *InsightsService) currentNewsList(ctx context.Context, docs []model.CrawledDocument) ([]model.CrawledDocument, error) {
	keys := []repository.NewsReadKey{}
	for _, doc := range docs {
		if doc.Type == "news" || strings.HasPrefix(doc.S3Key, "news/") {
			keys = append(keys, newsKeys(doc.SourceID, doc.DocHash)...)
		}
	}
	if len(keys) == 0 {
		return docs, nil
	}
	rows, err := s.newsRows(ctx, keys)
	if err != nil {
		return nil, err
	}
	result := make([]model.CrawledDocument, 0, len(docs))
	for _, doc := range docs {
		if doc.Type != "news" && !strings.HasPrefix(doc.S3Key, "news/") {
			result = append(result, doc)
			continue
		}
		selection := newsKeys(doc.SourceID, doc.DocHash)
		row, config := rows[selection[0]], rows[selection[1]]
		if len(row) == 0 {
			continue
		}
		if isVersionedNews(row) && !currentNewsPublication(row, config, doc.SourceID, doc.DocHash) {
			continue
		}
		current, err := decodeNewsDocument(row, doc.SourceID, doc.DocHash)
		if err != nil {
			return nil, err
		}
		result = append(result, current)
	}
	return result, nil
}
