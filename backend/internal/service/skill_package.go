package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"regexp"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/ttobak/backend/internal/model"
)

// ErrInvalidPackage wraps a *SkillPackageError (errors.As gives the rule).
var ErrInvalidPackage = errors.New("invalid skill package")

const (
	skillPackageUploadTTL   = 10 * time.Minute
	skillPackageDownloadTTL = 5 * time.Minute
	// Fixed width, so history sort keys order by time.
	skillPackageTimeLayout = "2006-01-02T15:04:05.000000000Z"
)

var (
	skillPackageUploadIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
	skillPackageSHAPattern      = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// SkillPackageRepository stores package rows. PutSkillPackage is conditional:
// previousSHA "" requires no row, otherwise the stored sha256 must match.
type SkillPackageRepository interface {
	GetSkillPackage(ctx context.Context, pk, sk string) (*model.SkillPackage, error)
	PutSkillPackage(ctx context.Context, row model.SkillPackage, previousSHA string) error
	ListSkillPackageVersions(ctx context.Context) ([]model.SkillPackage, error)
	// DeleteSkillPackage is conditional on expectedSHA unless it is "".
	DeleteSkillPackage(ctx context.Context, pk, sk, expectedSHA string) error
}

// SkillPackageStore holds the package zips. Size returns ErrNotFound for a
// missing object.
type SkillPackageStore interface {
	PresignUpload(ctx context.Context, key string, maxBytes int64, ttl time.Duration) (string, map[string]string, error)
	Size(ctx context.Context, key string) (int64, error)
	Read(ctx context.Context, key string, max int64) ([]byte, error)
	Put(ctx context.Context, key string, data []byte) error
	Delete(ctx context.Context, key string) error
	PresignDownload(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// SkillPackageView is GET /api/skill-packages: both rows, the merge and
// short-lived download URLs for the caller's packages only.
type SkillPackageView struct {
	Org       *model.SkillPackage `json:"org"`
	Personal  *model.SkillPackage `json:"personal"`
	Effective EffectivePackage    `json:"effective"`
	Downloads map[string]string   `json:"downloads"`
}

// SkillPackageResult reports a write and any failed best-effort cleanup.
type SkillPackageResult struct {
	Package  *model.SkillPackage `json:"package,omitempty"`
	Warnings []string            `json:"warnings"`
}

// SkillPackageService owns org and personal packages. Callers pass only the
// verified JWT user; org writes are admin-gated by the router.
type SkillPackageService struct {
	repo  SkillPackageRepository
	store SkillPackageStore
	now   func() time.Time
	newID func() string
}

func NewSkillPackageService(repo SkillPackageRepository, store SkillPackageStore) *SkillPackageService {
	return &SkillPackageService{repo: repo, store: store, now: time.Now, newID: randomUploadID}
}

func randomUploadID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func stagingKey(userID, uploadID string) string {
	return "skillpkg/staging/" + userID + "/" + uploadID + ".zip"
}

func packageKey(scope, userID, sha string) string {
	if scope == model.SkillPackageScopeOrg {
		return "skillpkg/org/" + sha + ".zip"
	}
	return "skillpkg/user/" + userID + "/" + sha + ".zip"
}

func packageRowKey(scope, userID string) (string, string) {
	if scope == model.SkillPackageScopeOrg {
		return model.SkillPackageOrgPK, model.SkillPackageCurrentSK
	}
	return model.SkillPackagePersonalPK(userID), model.SkillPackagePersonalSK
}

func validScope(scope string) bool {
	return scope == model.SkillPackageScopeOrg || scope == model.SkillPackageScopePersonal
}

func manifestOfRow(row *model.SkillPackage) *model.SkillPackageManifest {
	if row == nil {
		return nil
	}
	return &model.SkillPackageManifest{Version: row.Version, Skills: row.Skills, Servers: row.Servers}
}

// Get returns the org row, the caller's personal row, their merge and
// download URLs.
func (s *SkillPackageService) Get(ctx context.Context, userID string) (*SkillPackageView, error) {
	org, err := s.repo.GetSkillPackage(ctx, model.SkillPackageOrgPK, model.SkillPackageCurrentSK)
	if err != nil {
		return nil, err
	}
	personal, err := s.repo.GetSkillPackage(ctx, model.SkillPackagePersonalPK(userID), model.SkillPackagePersonalSK)
	if err != nil {
		return nil, err
	}
	view := &SkillPackageView{Org: org, Personal: personal, Downloads: map[string]string{},
		Effective: MergeSkillPackages(manifestOfRow(org), manifestOfRow(personal))}
	for scope, row := range map[string]*model.SkillPackage{model.SkillPackageScopeOrg: org, model.SkillPackageScopePersonal: personal} {
		if row == nil {
			continue
		}
		address, err := s.store.PresignDownload(ctx, row.ObjectKey, skillPackageDownloadTTL)
		if err != nil {
			return nil, err
		}
		view.Downloads[scope] = address
	}
	return view, nil
}

// StartUpload mints a presigned POST for a staging key under the caller.
func (s *SkillPackageService) StartUpload(ctx context.Context, userID, scope string) (*model.SkillPackageUpload, error) {
	if !validScope(scope) || userID == "" {
		return nil, ErrInvalidInput
	}
	id := s.newID()
	address, fields, err := s.store.PresignUpload(ctx, stagingKey(userID, id), model.SkillPackageMaxZipBytes, skillPackageUploadTTL)
	if err != nil {
		return nil, err
	}
	return &model.SkillPackageUpload{URL: address, Fields: fields, UploadID: id, ExpiresIn: int(skillPackageUploadTTL.Seconds())}, nil
}

// CompleteUpload validates the caller's staged zip, stores it by hash and
// makes it the scope's current package. The staged object is always removed.
func (s *SkillPackageService) CompleteUpload(ctx context.Context, userID, scope, uploadID string) (*SkillPackageResult, error) {
	if !validScope(scope) || userID == "" || !skillPackageUploadIDPattern.MatchString(uploadID) {
		return nil, ErrInvalidInput
	}
	staged := stagingKey(userID, uploadID)
	size, err := s.store.Size(ctx, staged)
	if err != nil {
		return nil, err
	}
	result := &SkillPackageResult{Warnings: []string{}}
	dropStaged := func() {
		if err := s.store.Delete(ctx, staged); err != nil {
			// Also logged: a rejected upload returns no result to carry the warning.
			log.Printf("skill package staging cleanup failed: %v", err)
			result.Warnings = append(result.Warnings, "staged upload not removed")
		}
	}
	if size > model.SkillPackageMaxZipBytes {
		dropStaged()
		return nil, errors.Join(ErrInvalidPackage, pkgErr(RuleZipTooLarge, ""))
	}
	data, err := s.store.Read(ctx, staged, model.SkillPackageMaxZipBytes+1)
	if err != nil {
		return nil, err
	}
	manifest, err := ValidateSkillPackage(data)
	if err != nil {
		dropStaged()
		return nil, errors.Join(ErrInvalidPackage, err)
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	key := packageKey(scope, userID, sha)
	// Publish the validated bytes themselves, never a copy of the staged
	// object: the presigned POST stays valid and could replace it meanwhile.
	if err := s.store.Put(ctx, key, data); err != nil {
		return nil, err
	}
	dropStaged()
	pk, sk := packageRowKey(scope, userID)
	row := model.SkillPackage{PK: pk, SK: sk, Scope: scope, Version: manifest.Version, SHA256: sha, Size: int64(len(data)),
		UploadedBy: userID, UploadedAt: s.now().UTC().Format(skillPackageTimeLayout), Skills: manifest.Skills,
		Servers: manifest.Servers, ObjectKey: key}
	previous, err := s.repo.GetSkillPackage(ctx, pk, sk)
	if err != nil {
		return nil, err
	}
	if scope == model.SkillPackageScopeOrg {
		version := row
		version.SK = model.SkillPackageVersionSK(row.UploadedAt, sha)
		if err := s.repo.PutSkillPackage(ctx, version, ""); err != nil {
			return nil, err
		}
	}
	if err := s.repo.PutSkillPackage(ctx, row, shaOrEmpty(previous)); err != nil {
		return nil, err
	}
	if scope == model.SkillPackageScopeOrg {
		result.Warnings = append(result.Warnings, s.pruneOrgVersions(ctx)...)
	} else if previous != nil && previous.ObjectKey != key {
		result.Warnings = append(result.Warnings, s.deleteUnreferenced(ctx, userID, previous.ObjectKey)...)
	}
	result.Package = &row
	return result, nil
}

func shaOrEmpty(row *model.SkillPackage) string {
	if row == nil {
		return ""
	}
	return row.SHA256
}

// pruneOrgVersions keeps the newest kept history rows and deletes older rows.
// Package objects are content-addressed and never deleted here: a rollback
// that commits after the history read could otherwise point the current row
// at an object being removed. Unreferenced org objects are left for a later
// cleanup pass.
func (s *SkillPackageService) pruneOrgVersions(ctx context.Context) []string {
	versions, err := s.repo.ListSkillPackageVersions(ctx)
	if err != nil {
		return []string{"old org versions not pruned"}
	}
	if len(versions) <= model.SkillPackageOrgVersionsKept {
		return nil
	}
	var warnings []string
	for _, old := range versions[model.SkillPackageOrgVersionsKept:] {
		if err := s.repo.DeleteSkillPackage(ctx, old.PK, old.SK, ""); err != nil {
			warnings = append(warnings, "old org version not pruned")
		}
	}
	return warnings
}

// deleteUnreferenced deletes a replaced personal object unless the caller's
// live row points at it again (a concurrent completion of the same bytes).
func (s *SkillPackageService) deleteUnreferenced(ctx context.Context, userID, key string) []string {
	pk, sk := packageRowKey(model.SkillPackageScopePersonal, userID)
	live, err := s.repo.GetSkillPackage(ctx, pk, sk)
	if err != nil {
		return []string{"previous package object not removed"}
	}
	if live != nil && live.ObjectKey == key {
		return nil
	}
	if err := s.store.Delete(ctx, key); err != nil {
		return []string{"previous package object not removed"}
	}
	return nil
}

// DeletePersonal removes the caller's personal package row, then its object.
func (s *SkillPackageService) DeletePersonal(ctx context.Context, userID string) (*SkillPackageResult, error) {
	pk, sk := packageRowKey(model.SkillPackageScopePersonal, userID)
	row, err := s.repo.GetSkillPackage(ctx, pk, sk)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, ErrNotFound
	}
	// Conditional on the row read above: a completion that replaced it in
	// between keeps its package (409).
	if err := s.repo.DeleteSkillPackage(ctx, pk, sk, row.SHA256); err != nil {
		return nil, err
	}
	result := &SkillPackageResult{Warnings: []string{}}
	result.Warnings = append(result.Warnings, s.deleteUnreferenced(ctx, userID, row.ObjectKey)...)
	return result, nil
}

// ListOrgVersions returns the kept org versions, newest first.
func (s *SkillPackageService) ListOrgVersions(ctx context.Context) ([]model.SkillPackage, error) {
	return s.repo.ListSkillPackageVersions(ctx)
}

// RollbackOrg makes a kept org version current again.
func (s *SkillPackageService) RollbackOrg(ctx context.Context, userID, sha string) (*model.SkillPackage, error) {
	if !skillPackageSHAPattern.MatchString(sha) {
		return nil, ErrInvalidInput
	}
	versions, err := s.repo.ListSkillPackageVersions(ctx)
	if err != nil {
		return nil, err
	}
	for _, version := range versions {
		if version.SHA256 != sha {
			continue
		}
		current, err := s.repo.GetSkillPackage(ctx, model.SkillPackageOrgPK, model.SkillPackageCurrentSK)
		if err != nil {
			return nil, err
		}
		row := version
		row.SK = model.SkillPackageCurrentSK
		if err := s.repo.PutSkillPackage(ctx, row, shaOrEmpty(current)); err != nil {
			return nil, err
		}
		return &row, nil
	}
	return nil, ErrNotFound
}

// s3SkillPackageStore keeps package zips in the application bucket.
type s3SkillPackageStore struct {
	client  *s3.Client
	presign *s3.PresignClient
	bucket  string
	sign    DownloadSigner
}

// DownloadSigner mints a download URL for an asset key: the shared signed
// CloudFront /media/* URL when signing material is available, else an S3
// presign (UploadService.GeneratePresignedDownloadURLWithTTL, ADR-027).
type DownloadSigner func(ctx context.Context, key string, ttl time.Duration) (string, error)

// NewS3SkillPackageStore stores packages under skillpkg/ in bucket.
func NewS3SkillPackageStore(client *s3.Client, bucket string, sign DownloadSigner) SkillPackageStore {
	return &s3SkillPackageStore{client: client, presign: s3.NewPresignClient(client), bucket: bucket, sign: sign}
}

func (s *s3SkillPackageStore) PresignUpload(ctx context.Context, key string, maxBytes int64, ttl time.Duration) (string, map[string]string, error) {
	request, err := s.presign.PresignPostObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)},
		func(options *s3.PresignPostOptions) {
			options.Expires = ttl
			options.Conditions = []any{[]any{"content-length-range", 1, maxBytes}}
		})
	if err != nil {
		return "", nil, fmt.Errorf("presign package upload: %w", err)
	}
	return request.URL, request.Values, nil
}

func (s *s3SkillPackageStore) Size(ctx context.Context, key string) (int64, error) {
	head, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		var missing *s3types.NotFound
		if errors.As(err, &missing) {
			return 0, ErrNotFound
		}
		return 0, fmt.Errorf("head package: %w", err)
	}
	return aws.ToInt64(head.ContentLength), nil
}

func (s *s3SkillPackageStore) Read(ctx context.Context, key string, max int64) ([]byte, error) {
	object, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		var missing *s3types.NoSuchKey
		if errors.As(err, &missing) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("read package: %w", err)
	}
	defer object.Body.Close()
	return io.ReadAll(io.LimitReader(object.Body, max))
}

func (s *s3SkillPackageStore) Put(ctx context.Context, key string, data []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key),
		Body: bytes.NewReader(data), ContentLength: aws.Int64(int64(len(data))), ContentType: aws.String("application/zip")})
	if err != nil {
		return fmt.Errorf("store package: %w", err)
	}
	return nil
}

func (s *s3SkillPackageStore) Delete(ctx context.Context, key string) error {
	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)}); err != nil {
		return fmt.Errorf("delete package: %w", err)
	}
	return nil
}

func (s *s3SkillPackageStore) PresignDownload(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return s.sign(ctx, key, ttl)
}
