package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagent"
	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrockagent/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/ttobak/backend/internal/model"
)

const MaxIndexObjectBytes int64 = 50 * 1024 * 1024

var (
	ErrIndexMissing        = errors.New("index source object missing")
	ErrIndexChanged        = errors.New("index source changed")
	ErrIndexInvalid        = errors.New("invalid index source")
	ErrIndexWriteUncertain = errors.New("projection write outcome uncertain")
	ErrIndexSyncBusy       = errors.New("another ingestion job is active")
	ErrIndexLeaseBusy      = errors.New("index coordinator lease is active")
)

type IndexObject struct {
	Key       string            `json:"key"`
	ETag      string            `json:"etag"`
	VersionID string            `json:"versionId,omitempty"`
	Size      int64             `json:"size"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Missing   bool              `json:"missing,omitempty"`
}
type IndexPart struct {
	Name, ContentType string
	Body              []byte
}
type IndexProviderJob struct {
	ID, Status     string
	Failed         int64
	FailureReasons []string
}

type IndexObjects interface {
	Head(context.Context, string) (IndexObject, error)
	Read(context.Context, IndexObject) ([]byte, error)
	Put(context.Context, string, string, []byte) error
	Delete(context.Context, string) error
	List(context.Context, string) ([]string, error)
	LegacyPage(context.Context, string) ([]string, string, error)
}
type IndexIngestion interface {
	Busy(context.Context) (bool, error)
	Start(context.Context, string) (string, error)
	Get(context.Context, string) (IndexProviderJob, error)
}

// IndexAWSProvider is pinned to the configured assets bucket, KB bucket and S3
// data source. It deliberately exposes no direct-ingestion or inference API.
type IndexAWSProvider struct {
	s3                                 *s3.Client
	bedrock                            *bedrockagent.Client
	assets, bucket, kbID, dataSourceID string
}

func NewIndexAWSProvider(objects *s3.Client, ingestion *bedrockagent.Client, assets, bucket, kbID, dataSourceID string) (*IndexAWSProvider, error) {
	if objects == nil || ingestion == nil || assets == "" || bucket == "" || assets == bucket || kbID == "" || dataSourceID == "" {
		return nil, fmt.Errorf("%w: indexing configuration is incomplete", ErrIndexInvalid)
	}
	return &IndexAWSProvider{objects, ingestion, assets, bucket, kbID, dataSourceID}, nil
}

func indexObjectError(err error) error {
	var missing *s3types.NoSuchKey
	var notFound *s3types.NotFound
	var api smithy.APIError
	if errors.As(err, &missing) || errors.As(err, &notFound) {
		return errors.Join(ErrIndexMissing, err)
	}
	if errors.As(err, &api) {
		switch api.ErrorCode() {
		case "NoSuchKey", "NotFound", "404":
			return errors.Join(ErrIndexMissing, err)
		case "PreconditionFailed":
			return errors.Join(ErrIndexChanged, err)
		}
	}
	return err
}

func (p *IndexAWSProvider) Head(ctx context.Context, key string) (IndexObject, error) {
	out, err := p.s3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(p.assets), Key: aws.String(key)})
	if err != nil {
		return IndexObject{}, indexObjectError(err)
	}
	if out.ETag == nil || out.ContentLength == nil {
		return IndexObject{}, ErrIndexInvalid
	}
	return IndexObject{Key: key, ETag: *out.ETag, VersionID: aws.ToString(out.VersionId), Size: *out.ContentLength, Metadata: out.Metadata}, nil
}

func (p *IndexAWSProvider) Read(ctx context.Context, object IndexObject) ([]byte, error) {
	if object.ETag == "" || object.Size < 0 || object.Size > MaxIndexObjectBytes {
		return nil, ErrIndexInvalid
	}
	input := &s3.GetObjectInput{Bucket: aws.String(p.assets), Key: aws.String(object.Key), IfMatch: aws.String(object.ETag)}
	if object.VersionID != "" {
		input.VersionId = aws.String(object.VersionID)
	}
	out, err := p.s3.GetObject(ctx, input)
	if err != nil {
		return nil, indexObjectError(err)
	}
	defer out.Body.Close()
	if aws.ToString(out.ETag) != object.ETag || (object.VersionID != "" && aws.ToString(out.VersionId) != object.VersionID) {
		return nil, ErrIndexChanged
	}
	body, err := io.ReadAll(io.LimitReader(out.Body, object.Size+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) != object.Size || int64(len(body)) > MaxIndexObjectBytes {
		return nil, ErrIndexChanged
	}
	return body, nil
}

func indexProjectionKey(key string) bool {
	if !strings.HasPrefix(key, model.IndexPrefix) && !strings.HasPrefix(key, "meetings/") {
		return false
	}
	for _, component := range strings.Split(key, "/") {
		if component == "" || component == "." || component == ".." {
			return false
		}
	}
	return true
}
func (p *IndexAWSProvider) Put(ctx context.Context, key, contentType string, body []byte) error {
	if !strings.HasPrefix(key, model.IndexPrefix) || !indexProjectionKey(key) {
		return ErrIndexInvalid
	}
	_, err := p.s3.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(p.bucket), Key: aws.String(key), Body: bytes.NewReader(body),
		ContentType: aws.String(contentType), IfNoneMatch: aws.String("*")},
		func(o *s3.Options) { o.Retryer = aws.NopRetryer{} })
	if err != nil {
		return errors.Join(ErrIndexWriteUncertain, err)
	}
	return nil
}
func (p *IndexAWSProvider) Delete(ctx context.Context, key string) error {
	if !indexProjectionKey(key) {
		return ErrIndexInvalid
	}
	_, err := p.s3.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(p.bucket), Key: aws.String(key)})
	return err
}
func (p *IndexAWSProvider) List(ctx context.Context, prefix string) ([]string, error) {
	if !strings.HasPrefix(prefix, model.IndexPrefix) && !strings.HasPrefix(prefix, "meetings/") {
		return nil, ErrIndexInvalid
	}
	pages := s3.NewListObjectsV2Paginator(p.s3, &s3.ListObjectsV2Input{Bucket: aws.String(p.bucket), Prefix: aws.String(prefix)})
	keys := []string{}
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, object := range page.Contents {
			keys = append(keys, aws.ToString(object.Key))
		}
	}
	return keys, nil
}
func (p *IndexAWSProvider) LegacyPage(ctx context.Context, cursor string) ([]string, string, error) {
	input := &s3.ListObjectsV2Input{Bucket: aws.String(p.bucket), Prefix: aws.String("meetings/"), MaxKeys: aws.Int32(100)}
	if cursor != "" {
		input.ContinuationToken = aws.String(cursor)
	}
	page, err := p.s3.ListObjectsV2(ctx, input)
	if err != nil {
		return nil, "", err
	}
	keys := []string{}
	for _, object := range page.Contents {
		keys = append(keys, aws.ToString(object.Key))
	}
	return keys, aws.ToString(page.NextContinuationToken), nil
}

func (p *IndexAWSProvider) Busy(ctx context.Context) (bool, error) {
	pages := bedrockagent.NewListIngestionJobsPaginator(p.bedrock, &bedrockagent.ListIngestionJobsInput{
		KnowledgeBaseId: aws.String(p.kbID), DataSourceId: aws.String(p.dataSourceID)})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return false, err
		}
		for _, job := range page.IngestionJobSummaries {
			switch string(job.Status) {
			case "STARTING", "IN_PROGRESS", "STOPPING":
				return true, nil
			case "COMPLETE", "FAILED", "STOPPED":
			default:
				return false, ErrIndexInvalid
			}
		}
	}
	return false, nil
}
func (p *IndexAWSProvider) Start(ctx context.Context, token string) (string, error) {
	if len(token) < 33 {
		return "", ErrIndexInvalid
	}
	out, err := p.bedrock.StartIngestionJob(ctx, &bedrockagent.StartIngestionJobInput{
		KnowledgeBaseId: aws.String(p.kbID), DataSourceId: aws.String(p.dataSourceID),
		ClientToken: aws.String(token), Description: aws.String("canonical-index-" + token)},
		func(o *bedrockagent.Options) { o.Retryer = aws.NopRetryer{} })
	var conflict *bedrocktypes.ConflictException
	if errors.As(err, &conflict) {
		return "", errors.Join(ErrIndexSyncBusy, err)
	}
	if err != nil {
		return "", err
	}
	if out.IngestionJob == nil || aws.ToString(out.IngestionJob.IngestionJobId) == "" ||
		aws.ToString(out.IngestionJob.KnowledgeBaseId) != p.kbID || aws.ToString(out.IngestionJob.DataSourceId) != p.dataSourceID {
		return "", ErrIndexInvalid
	}
	return *out.IngestionJob.IngestionJobId, nil
}
func (p *IndexAWSProvider) Get(ctx context.Context, id string) (IndexProviderJob, error) {
	out, err := p.bedrock.GetIngestionJob(ctx, &bedrockagent.GetIngestionJobInput{
		KnowledgeBaseId: aws.String(p.kbID), DataSourceId: aws.String(p.dataSourceID), IngestionJobId: aws.String(id)})
	if err != nil {
		return IndexProviderJob{}, err
	}
	job := out.IngestionJob
	if job == nil || aws.ToString(job.IngestionJobId) != id || aws.ToString(job.KnowledgeBaseId) != p.kbID ||
		aws.ToString(job.DataSourceId) != p.dataSourceID || (string(job.Status) == "COMPLETE" && job.Statistics == nil) {
		return IndexProviderJob{}, ErrIndexInvalid
	}
	result := IndexProviderJob{ID: id, Status: string(job.Status), FailureReasons: job.FailureReasons}
	if job.Statistics != nil {
		result.Failed = job.Statistics.NumberOfDocumentsFailed
	}
	return result, nil
}

// IndexOperationTimeout bounds each HTTP attempt independently of job leases.
const IndexOperationTimeout = 30 * time.Second
