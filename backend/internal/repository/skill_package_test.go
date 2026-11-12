package repository

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func TestPutSkillPackageIsConditional(test *testing.T) {
	var conditions []string
	failNext := false
	repo := summarySDKRepo(func(request *http.Request) (*http.Response, error) {
		body := performanceBody(test, request)
		if performanceTarget(request) != "PutItem" {
			test.Fatalf("unexpected call %s", performanceTarget(request))
		}
		conditions = append(conditions, body["ConditionExpression"].(string))
		if failNext {
			return summaryHTTPResponse(400, `{"__type":"com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException","message":"x"}`, nil), nil
		}
		return summaryHTTPResponse(200, `{}`, nil), nil
	})
	row := model.SkillPackage{PK: model.SkillPackageOrgPK, SK: model.SkillPackageCurrentSK, SHA256: strings.Repeat("a", 64)}
	if err := repo.PutSkillPackage(test.Context(), row, ""); err != nil {
		test.Fatal(err)
	}
	if err := repo.PutSkillPackage(test.Context(), row, strings.Repeat("b", 64)); err != nil {
		test.Fatal(err)
	}
	if !strings.Contains(conditions[0], "attribute_not_exists") || !strings.Contains(conditions[1], "=") {
		test.Fatalf("conditions = %v", conditions)
	}
	failNext = true
	if err := repo.PutSkillPackage(test.Context(), row, ""); !errors.Is(err, ErrConditionFailed) {
		test.Fatalf("err = %v", err)
	}
}

func TestListSkillPackageVersionsNewestFirst(test *testing.T) {
	repo := summarySDKRepo(func(request *http.Request) (*http.Response, error) {
		body := performanceBody(test, request)
		if performanceTarget(request) != "Query" || body["ScanIndexForward"] != false {
			test.Fatalf("query = %v", body)
		}
		return summaryHTTPResponse(200, `{"Items":[{"PK":{"S":"SKILLPKG#org"},"SK":{"S":"VERSION#2026-11-12T00:00:00Z#`+strings.Repeat("a", 64)+`"},"sha256":{"S":"`+strings.Repeat("a", 64)+`"}}]}`, nil), nil
	})
	versions, err := repo.ListSkillPackageVersions(test.Context())
	if err != nil || len(versions) != 1 || versions[0].SHA256 != strings.Repeat("a", 64) {
		test.Fatalf("versions = %v, %v", versions, err)
	}
}
