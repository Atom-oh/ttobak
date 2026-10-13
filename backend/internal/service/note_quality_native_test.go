package service

// Native evaluation records at the SDK HTTP boundary. The production service
// owns provider selection, request serialization, continuation and publication;
// only the repository is synthetic. Replay never opens a network connection.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

const qualityNativeSchema = 2
const qualityWireLimit = 8 * 1024 * 1024

var (
	errQualityRequestExport = errors.New("request export only; no model invocation")
	errQualityNativeMissing = errors.New("native evidence missing; legacy evidence requires historical-grade")
)

type qualityBinding struct {
	ModelID            string `json:"modelId"`
	Region             string `json:"region"`
	CorpusSHA256       string `json:"corpusSha256"`
	SourceSHA256       string `json:"sourceSha256"`
	ProductionSHA256   string `json:"productionSourceSha256"`
	EvaluatorSHA256    string `json:"evaluatorSourceSha256"`
	DeploymentSHA256   string `json:"deploymentConfigSha256,omitempty"`
	DeployedCodeSHA256 string `json:"deployedCodeSha256,omitempty"`
}

type qualityNativeCall struct {
	Sequence       int               `json:"sequence"`
	BindingSHA256  string            `json:"bindingSha256"`
	ModelID        string            `json:"modelId"`
	RequestSHA256  string            `json:"requestSha256"`
	ResponseSHA256 string            `json:"responseSha256,omitempty"`
	Status         int               `json:"status,omitempty"`
	Headers        map[string]string `json:"responseHeaders,omitempty"`
	Failure        string            `json:"failure,omitempty"`
	ObservedAt     time.Time         `json:"observedAt"`
	Request        []byte            `json:"-"`
	Response       []byte            `json:"-"`
}

type qualityNativeEvidence struct {
	SchemaVersion int                 `json:"schemaVersion"`
	Transport     string              `json:"transport"`
	Binding       qualityBinding      `json:"binding"`
	PromptSHA256  string              `json:"promptSha256"`
	Completed     bool                `json:"completed"`
	OutputSHA256  string              `json:"outputSha256,omitempty"`
	Calls         []qualityNativeCall `json:"calls"`
}

type qualityDeployment struct {
	SummaryModelID   string `json:"summaryModelId"`
	AuxiliaryModelID string `json:"auxiliaryModelId"`
	Region           string `json:"region"`
	CodeSHA256       string `json:"codeSha256"`
	State            string `json:"state"`
	LastUpdateStatus string `json:"lastUpdateStatus"`
	RevisionID       string `json:"revisionId"`
}

// NewBedrockService is the authoritative model resolver. This includes the
// auxiliary/default fallback captured when production initializes the package.
func qualityEffectiveModel() string { return NewBedrockService(nil, nil, nil).summaryModelID }

func qualityRegion() string { return getEnvOrDefault("BEDROCK_REGION", "us-west-2") }

func qualityValidateDeployment(data []byte) (qualityDeployment, error) {
	var record qualityDeployment
	if json.Unmarshal(data, &record) != nil || record.State != "Active" ||
		record.LastUpdateStatus != "Successful" || record.RevisionID == "" {
		return record, errors.New("deployment snapshot missing or unstable")
	}
	code, err := base64.StdEncoding.DecodeString(record.CodeSHA256)
	if err != nil || len(code) != sha256.Size {
		return record, errors.New("deployment snapshot requires a valid code SHA-256")
	}
	if record.SummaryModelID != os.Getenv("BEDROCK_SUMMARY_MODEL_ID") ||
		record.AuxiliaryModelID != os.Getenv("BEDROCK_MODEL_ID") ||
		(record.Region != "" && record.Region != qualityRegion()) ||
		(record.Region == "" && qualityRegion() != "us-west-2") {
		return record, errors.New("evaluation configuration differs from deployment snapshot")
	}
	return record, nil
}

func qualitySourceDigests() (production, evaluator string, err error) {
	productionFiles := []string{"../../go.mod", "../../go.sum"}
	var evaluatorFiles []string
	for _, root := range []string{"..", "../../cmd"} {
		err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			if strings.HasSuffix(path, "_test.go") {
				if strings.HasPrefix(filepath.Base(path), "note_quality_") {
					evaluatorFiles = append(evaluatorFiles, path)
				}
			} else {
				productionFiles = append(productionFiles, path)
			}
			return nil
		})
		if err != nil {
			return "", "", err
		}
	}
	hashFiles := func(files []string) (string, error) {
		sort.Strings(files)
		hash := sha256.New()
		for _, path := range files {
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return "", readErr
			}
			_, _ = fmt.Fprintf(hash, "%s\x00%d\x00", filepath.ToSlash(path), len(data))
			_, _ = hash.Write(data)
		}
		return hex.EncodeToString(hash.Sum(nil)), nil
	}
	production, err = hashFiles(productionFiles)
	if err != nil {
		return "", "", err
	}
	evaluator, err = hashFiles(evaluatorFiles)
	return production, evaluator, err
}

func qualityBindingHash(binding qualityBinding) string {
	body, _ := json.Marshal(binding)
	return qualityHash(body)
}

type qualityNativeTransport struct {
	base     aws.HTTPClient
	evidence *qualityNativeEvidence
	replay   bool
	export   bool
	position int
	err      error
}

func (transport *qualityNativeTransport) Do(request *http.Request) (*http.Response, error) {
	if transport.err != nil {
		return nil, transport.err
	}
	fail := func(message string) (*http.Response, error) {
		transport.err = errors.New(message)
		return nil, transport.err
	}
	modelID := strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, "/model/"), "/invoke")
	if request.Method != http.MethodPost || !strings.HasPrefix(request.URL.Path, "/model/") ||
		!strings.HasSuffix(request.URL.Path, "/invoke") || modelID != transport.evidence.Binding.ModelID {
		return fail("native request changed the bound model or operation")
	}
	raw, err := io.ReadAll(io.LimitReader(request.Body, qualityWireLimit+1))
	if err != nil || len(raw) > qualityWireLimit {
		return fail("native request read failed or exceeded limit")
	}
	request.Body = io.NopCloser(bytes.NewReader(raw))
	if transport.replay {
		if transport.position >= len(transport.evidence.Calls) {
			return fail("missing continuation response")
		}
		call := transport.evidence.Calls[transport.position]
		transport.position++
		if call.ModelID != modelID || !bytes.Equal(call.Request, raw) {
			return fail("native continuation request does not match recorded bytes")
		}
		if call.Failure != "" {
			return nil, errors.New("recorded transport failure")
		}
		headers := http.Header{}
		for name, value := range call.Headers {
			headers.Set(name, value)
		}
		return &http.Response{StatusCode: call.Status, Header: headers, Body: io.NopCloser(bytes.NewReader(call.Response))}, nil
	}
	call := qualityNativeCall{Sequence: len(transport.evidence.Calls) + 1, ModelID: modelID,
		BindingSHA256: qualityBindingHash(transport.evidence.Binding), Request: append([]byte(nil), raw...),
		RequestSHA256: qualityHash(raw), ObservedAt: time.Now().UTC()}
	transport.evidence.Calls = append(transport.evidence.Calls, call)
	if len(transport.evidence.Calls) == 1 {
		transport.evidence.PromptSHA256 = call.RequestSHA256
	}
	if transport.export {
		return nil, errQualityRequestExport
	}
	if transport.base == nil {
		return fail("native recording has no provider transport")
	}
	index := len(transport.evidence.Calls) - 1
	response, err := transport.base.Do(request)
	if err != nil || response == nil || response.Body == nil {
		transport.evidence.Calls[index].Failure = "transport_error"
		return nil, errors.New("provider transport failed")
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, qualityWireLimit+1))
	if readErr != nil || len(body) > qualityWireLimit {
		transport.evidence.Calls[index].Failure = "response_read_error"
		return nil, errors.New("provider response read failed or exceeded limit")
	}
	record := &transport.evidence.Calls[index]
	record.Status, record.Response, record.ResponseSHA256 = response.StatusCode, append([]byte(nil), body...), qualityHash(body)
	record.Headers = map[string]string{}
	for _, name := range []string{"Content-Type", "X-Amzn-Errortype"} {
		if value := response.Header.Get(name); value != "" {
			record.Headers[name] = value
		}
	}
	copy := *response
	copy.Body = io.NopCloser(bytes.NewReader(body))
	return &copy, nil
}

func qualityNativeClient(transport *qualityNativeTransport, live *bedrockruntime.Client) *bedrockruntime.Client {
	if live != nil {
		options := live.Options()
		transport.base, options.HTTPClient = options.HTTPClient, transport
		return bedrockruntime.New(options)
	}
	return bedrockruntime.New(bedrockruntime.Options{Region: transport.evidence.Binding.Region, HTTPClient: transport,
		Retryer: aws.NopRetryer{}, RetryMaxAttempts: 1,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "evaluation-fixture", SecretAccessKey: "evaluation-fixture"}, nil
		})})
}

func qualityFixtureRepository(t *testing.T, meeting *model.Meeting) *repository.DynamoDBRepository {
	t.Helper()
	row := *meeting
	row.PK, row.SK = model.PrefixUser+row.UserID, model.PrefixMeeting+row.MeetingID
	item, err := attributevalue.MarshalMap(row)
	if err != nil {
		t.Fatal(err)
	}
	body, err := attributevalue.MarshalMapJSON(item)
	if err != nil {
		t.Fatal(err)
	}
	client := dynamodb.New(dynamodb.Options{Region: "us-west-2", Credentials: aws.AnonymousCredentials{},
		Retryer: aws.NopRetryer{}, HTTPClient: noteSourceHTTPClient(func(request *http.Request) (*http.Response, error) {
			response := `{}`
			switch request.Header.Get("X-Amz-Target") {
			case "DynamoDB_20120810.GetItem":
				response = `{"Item":` + string(body) + `}`
			case "DynamoDB_20120810.Query":
				response = `{"Items":[]}`
			case "DynamoDB_20120810.UpdateItem", "DynamoDB_20120810.TransactWriteItems":
			default:
				return nil, errors.New("unexpected synthetic repository operation")
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
				Body: io.NopCloser(strings.NewReader(response))}, nil
		})})
	return repository.NewDynamoDBRepository(client, "synthetic-note-evaluation")
}

func qualityRunNative(t *testing.T, ctx context.Context, fixture qualityCase, transport *qualityNativeTransport, live *bedrockruntime.Client) (string, error) {
	t.Helper()
	meeting := qualityMeeting(fixture)
	service := NewBedrockService(qualityNativeClient(transport, live), nil, qualityFixtureRepository(t, meeting))
	if service.summaryModelID != transport.evidence.Binding.ModelID {
		return "", errors.New("effective production model differs from binding")
	}
	note, err := service.SummarizeTranscript(ctx, meeting.MeetingID, meeting.UserID, "")
	if transport.err != nil {
		return "", transport.err
	}
	if transport.replay && transport.position != len(transport.evidence.Calls) {
		return "", errors.New("unused recorded model responses")
	}
	return note, err
}

func qualityExportNative(t *testing.T, fixture qualityCase, binding qualityBinding) (*qualityNativeEvidence, error) {
	evidence := &qualityNativeEvidence{SchemaVersion: qualityNativeSchema, Transport: "export", Binding: binding}
	transport := &qualityNativeTransport{evidence: evidence, export: true}
	_, err := qualityRunNative(t, context.Background(), fixture, transport, nil)
	if err == nil || transport.err != nil || len(evidence.Calls) != 1 || evidence.Calls[0].Response != nil {
		return nil, errors.New("request export did not stop at the native provider boundary")
	}
	return evidence, nil
}

func qualityValidateNativeEvidence(evidence *qualityNativeEvidence, expected qualityBinding, firstRequest []byte) error {
	if evidence.SchemaVersion != qualityNativeSchema || evidence.Transport != "aws-sdk" ||
		!reflect.DeepEqual(evidence.Binding, expected) || evidence.PromptSHA256 != qualityHash(firstRequest) ||
		len(evidence.Calls) == 0 || len(evidence.Calls) > maxSummaryContinuations+1 {
		return errors.New("native evaluation evidence does not match current source/model/deployment")
	}
	for index, call := range evidence.Calls {
		if call.Sequence != index+1 || call.ModelID != expected.ModelID ||
			call.BindingSHA256 != qualityBindingHash(expected) || call.ObservedAt.IsZero() ||
			call.RequestSHA256 != qualityHash(call.Request) || len(call.Request) == 0 || len(call.Request) > qualityWireLimit ||
			len(call.Response) > qualityWireLimit {
			return errors.New("model call identity/order/request evidence invalid")
		}
		if index == 0 && !bytes.Equal(call.Request, firstRequest) {
			return errors.New("initial native request changed")
		}
		if call.Failure != "" {
			if (call.Failure != "transport_error" && call.Failure != "response_read_error") || len(call.Response) != 0 ||
				call.ResponseSHA256 != "" || call.Status != 0 || index != len(evidence.Calls)-1 || evidence.Completed {
				return errors.New("invalid recorded transport failure")
			}
		} else if call.ResponseSHA256 != qualityHash(call.Response) || call.Status < 100 || call.Status > 599 {
			return errors.New("native response evidence invalid")
		}
		for name := range call.Headers {
			if name != "Content-Type" && name != "X-Amzn-Errortype" {
				return errors.New("unexpected recorded response header")
			}
		}
	}
	if evidence.Completed && (len(evidence.OutputSHA256) != 64 || evidence.OutputSHA256 == qualityHash(nil)) {
		return errors.New("completed native evidence requires a nonempty output hash")
	}
	return nil
}

func qualitySaveNative(t *testing.T, directory, id string, evidence *qualityNativeEvidence) {
	t.Helper()
	for _, call := range evidence.Calls {
		prefix := fmt.Sprintf("%s.call-%02d", id, call.Sequence)
		writeQualityFile(t, directory, prefix+".request.json", call.Request)
		if call.Failure == "" && call.Status != 0 {
			writeQualityFile(t, directory, prefix+".response.json", call.Response)
		}
	}
	writeQualityFile(t, directory, id+".native-evidence.json", qualityJSON(t, evidence))
}

func qualityReadBounded(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("evaluation artifact must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, qualityWireLimit+1))
	if err != nil || len(data) > qualityWireLimit {
		return nil, errors.New("evaluation artifact read failed or exceeded limit")
	}
	return data, nil
}

func qualityLoadNative(directory, id string) (*qualityNativeEvidence, error) {
	body, err := qualityReadBounded(filepath.Join(directory, id+".native-evidence.json"))
	if err != nil {
		return nil, errQualityNativeMissing
	}
	var evidence qualityNativeEvidence
	if json.Unmarshal(body, &evidence) != nil || evidence.SchemaVersion != qualityNativeSchema ||
		len(evidence.Calls) > maxSummaryContinuations+1 {
		return nil, errors.New("invalid native evidence manifest")
	}
	for index := range evidence.Calls {
		call := &evidence.Calls[index]
		prefix := fmt.Sprintf("%s.call-%02d", id, index+1)
		call.Request, err = qualityReadBounded(filepath.Join(directory, prefix+".request.json"))
		if err != nil {
			return nil, err
		}
		if call.Failure == "" {
			call.Response, err = qualityReadBounded(filepath.Join(directory, prefix+".response.json"))
			if err != nil {
				return nil, err
			}
		}
	}
	return &evidence, nil
}
