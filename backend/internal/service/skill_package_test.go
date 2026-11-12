package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type fakeSkillPackageRepo struct {
	rows map[string]model.SkillPackage
}

func (f *fakeSkillPackageRepo) key(pk, sk string) string { return pk + "|" + sk }

func (f *fakeSkillPackageRepo) GetSkillPackage(_ context.Context, pk, sk string) (*model.SkillPackage, error) {
	row, ok := f.rows[f.key(pk, sk)]
	if !ok {
		return nil, nil
	}
	return &row, nil
}

func (f *fakeSkillPackageRepo) PutSkillPackage(_ context.Context, row model.SkillPackage, previousSHA string) error {
	current, ok := f.rows[f.key(row.PK, row.SK)]
	if (previousSHA == "" && ok) || (previousSHA != "" && (!ok || current.SHA256 != previousSHA)) {
		return repository.ErrConditionFailed
	}
	f.rows[f.key(row.PK, row.SK)] = row
	return nil
}

func (f *fakeSkillPackageRepo) ListSkillPackageVersions(_ context.Context) ([]model.SkillPackage, error) {
	var out []model.SkillPackage
	for _, row := range f.rows {
		if row.PK == model.SkillPackageOrgPK && strings.HasPrefix(row.SK, model.PrefixSkillPackageVersion) {
			out = append(out, row)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SK > out[j].SK })
	return out, nil
}

func (f *fakeSkillPackageRepo) DeleteSkillPackage(_ context.Context, pk, sk string) error {
	delete(f.rows, f.key(pk, sk))
	return nil
}

type fakeSkillPackageStore struct {
	objects map[string][]byte
	deleted []string
}

func (f *fakeSkillPackageStore) PresignUpload(_ context.Context, key string, maxBytes int64, ttl time.Duration) (string, map[string]string, error) {
	return "https://bucket.example/", map[string]string{"key": key}, nil
}

func (f *fakeSkillPackageStore) Size(_ context.Context, key string) (int64, error) {
	data, ok := f.objects[key]
	if !ok {
		return 0, ErrNotFound
	}
	return int64(len(data)), nil
}

func (f *fakeSkillPackageStore) Read(_ context.Context, key string, max int64) ([]byte, error) {
	data, ok := f.objects[key]
	if !ok {
		return nil, ErrNotFound
	}
	if int64(len(data)) > max {
		return data[:max], nil
	}
	return data, nil
}

func (f *fakeSkillPackageStore) Copy(_ context.Context, from, to string) error {
	f.objects[to] = f.objects[from]
	return nil
}

func (f *fakeSkillPackageStore) Delete(_ context.Context, key string) error {
	delete(f.objects, key)
	f.deleted = append(f.deleted, key)
	return nil
}

func (f *fakeSkillPackageStore) PresignDownload(_ context.Context, key string, ttl time.Duration) (string, error) {
	return "https://bucket.example/" + key, nil
}

const testUploadID = "0123456789abcdef0123456789abcdef"

func newSkillPackageFixture(t *testing.T) (*SkillPackageService, *fakeSkillPackageRepo, *fakeSkillPackageStore) {
	t.Helper()
	repo := &fakeSkillPackageRepo{rows: map[string]model.SkillPackage{}}
	store := &fakeSkillPackageStore{objects: map[string][]byte{}}
	svc := NewSkillPackageService(repo, store)
	tick := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { tick = tick.Add(time.Minute); return tick }
	svc.newID = func() string { return testUploadID }
	return svc, repo, store
}

func stage(store *fakeSkillPackageStore, user string, data []byte) {
	store.objects["skillpkg/staging/"+user+"/"+testUploadID+".zip"] = data
}

func packageZip(t *testing.T, skill string) []byte {
	return buildZip(t, []zipEntry{{name: "pkg/skills/" + skill + "/SKILL.md", data: skill}})
}

func shaOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestCompleteUploadStoresByHash(t *testing.T) {
	svc, repo, store := newSkillPackageFixture(t)
	data := packageZip(t, "demo-skill")
	stage(store, "alice", data)
	result, err := svc.CompleteUpload(t.Context(), "alice", model.SkillPackageScopePersonal, testUploadID)
	if err != nil {
		t.Fatal(err)
	}
	want := "skillpkg/user/alice/" + shaOf(data) + ".zip"
	if _, ok := store.objects[want]; !ok || result.Package.SHA256 != shaOf(data) {
		t.Fatalf("object %s missing or sha wrong: %+v", want, result.Package)
	}
	if _, ok := store.objects["skillpkg/staging/alice/"+testUploadID+".zip"]; ok {
		t.Fatal("staging object kept")
	}
	row, _ := repo.GetSkillPackage(t.Context(), "USER#alice", model.SkillPackagePersonalSK)
	if row == nil || row.ObjectKey != want || row.Skills[0] != "demo-skill" {
		t.Fatalf("row = %+v", row)
	}
}

func TestCompleteUploadRejectsOversizeObject(t *testing.T) {
	svc, _, store := newSkillPackageFixture(t)
	stage(store, "alice", make([]byte, model.SkillPackageMaxZipBytes+1))
	_, err := svc.CompleteUpload(t.Context(), "alice", model.SkillPackageScopePersonal, testUploadID)
	var pkg *SkillPackageError
	if !errors.Is(err, ErrInvalidPackage) || !errors.As(err, &pkg) || pkg.Rule != RuleZipTooLarge {
		t.Fatalf("err = %v", err)
	}
	if len(store.objects) != 0 {
		t.Fatalf("staging object kept: %v", store.deleted)
	}
}

func TestCompleteUploadRejectsBadUploadID(t *testing.T) {
	svc, _, _ := newSkillPackageFixture(t)
	for _, id := range []string{"../x", strings.ToUpper(testUploadID), testUploadID[:31], ""} {
		if _, err := svc.CompleteUpload(t.Context(), "alice", model.SkillPackageScopePersonal, id); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%q: err = %v", id, err)
		}
	}
}

func TestCompleteUploadRejectsForeignUploadID(t *testing.T) {
	svc, _, store := newSkillPackageFixture(t)
	stage(store, "bob", packageZip(t, "demo-skill"))
	if _, err := svc.CompleteUpload(t.Context(), "alice", model.SkillPackageScopePersonal, testUploadID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestPersonalPackageIsOwnerScoped(t *testing.T) {
	svc, _, store := newSkillPackageFixture(t)
	stage(store, "alice", packageZip(t, "mine"))
	if _, err := svc.CompleteUpload(t.Context(), "alice", model.SkillPackageScopePersonal, testUploadID); err != nil {
		t.Fatal(err)
	}
	view, err := svc.Get(t.Context(), "bob")
	if err != nil {
		t.Fatal(err)
	}
	if view.Personal != nil || len(view.Effective.Skills) != 0 || view.Downloads[model.SkillPackageScopePersonal] != "" {
		t.Fatalf("bob sees alice's package: %+v", view)
	}
	own, _ := svc.Get(t.Context(), "alice")
	if own.Personal == nil || own.Effective.Skills[0].Source != model.SkillPackageScopePersonal || own.Downloads["personal"] == "" {
		t.Fatalf("alice view = %+v", own)
	}
}

func TestDeletePersonalFallsBackToOrg(t *testing.T) {
	svc, _, store := newSkillPackageFixture(t)
	stage(store, "admin", packageZip(t, "shared"))
	if _, err := svc.CompleteUpload(t.Context(), "admin", model.SkillPackageScopeOrg, testUploadID); err != nil {
		t.Fatal(err)
	}
	stage(store, "alice", packageZip(t, "shared"))
	if _, err := svc.CompleteUpload(t.Context(), "alice", model.SkillPackageScopePersonal, testUploadID); err != nil {
		t.Fatal(err)
	}
	if view, _ := svc.Get(t.Context(), "alice"); view.Effective.Skills[0].Source != "personal" {
		t.Fatalf("personal must win: %+v", view.Effective)
	}
	if _, err := svc.DeletePersonal(t.Context(), "alice"); err != nil {
		t.Fatal(err)
	}
	view, _ := svc.Get(t.Context(), "alice")
	if view.Personal != nil || view.Effective.Skills[0].Source != "org" {
		t.Fatalf("after delete: %+v", view)
	}
	if _, err := svc.DeletePersonal(t.Context(), "alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestOrgHistoryKeepsFive(t *testing.T) {
	svc, repo, store := newSkillPackageFixture(t)
	var keys []string
	for i := 0; i < 7; i++ {
		data := packageZip(t, "skill-"+string(rune('a'+i)))
		stage(store, "admin", data)
		if _, err := svc.CompleteUpload(t.Context(), "admin", model.SkillPackageScopeOrg, testUploadID); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, "skillpkg/org/"+shaOf(data)+".zip")
	}
	versions, _ := repo.ListSkillPackageVersions(t.Context())
	if len(versions) != model.SkillPackageOrgVersionsKept {
		t.Fatalf("versions = %d", len(versions))
	}
	for i, key := range keys {
		_, present := store.objects[key]
		if present != (i >= 2) {
			t.Fatalf("object %d present=%v", i, present)
		}
	}
	current, _ := repo.GetSkillPackage(t.Context(), model.SkillPackageOrgPK, model.SkillPackageCurrentSK)
	if current.ObjectKey != keys[6] {
		t.Fatalf("current = %s", current.ObjectKey)
	}
}

func TestRollbackOrg(t *testing.T) {
	svc, repo, store := newSkillPackageFixture(t)
	var shas []string
	for _, skill := range []string{"first", "second"} {
		data := packageZip(t, skill)
		stage(store, "admin", data)
		if _, err := svc.CompleteUpload(t.Context(), "admin", model.SkillPackageScopeOrg, testUploadID); err != nil {
			t.Fatal(err)
		}
		shas = append(shas, shaOf(data))
	}
	if _, err := svc.RollbackOrg(t.Context(), "admin", strings.Repeat("0", 64)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown sha: %v", err)
	}
	if _, err := svc.RollbackOrg(t.Context(), "admin", "bad"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad sha: %v", err)
	}
	if _, err := svc.RollbackOrg(t.Context(), "admin", shas[0]); err != nil {
		t.Fatal(err)
	}
	current, _ := repo.GetSkillPackage(t.Context(), model.SkillPackageOrgPK, model.SkillPackageCurrentSK)
	if current.SHA256 != shas[0] || current.Skills[0] != "first" {
		t.Fatalf("current = %+v", current)
	}
}

func TestStartUploadScopesTheKey(t *testing.T) {
	svc, _, _ := newSkillPackageFixture(t)
	upload, err := svc.StartUpload(t.Context(), "alice", model.SkillPackageScopePersonal)
	if err != nil {
		t.Fatal(err)
	}
	if upload.UploadID != testUploadID || upload.Fields["key"] != "skillpkg/staging/alice/"+testUploadID+".zip" {
		t.Fatalf("upload = %+v", upload)
	}
	if _, err := svc.StartUpload(t.Context(), "alice", "other"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("scope: %v", err)
	}
}
