package service

import (
	"reflect"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func manifestOf(skills []string, servers model.SkillPackageServers) *model.SkillPackageManifest {
	return &model.SkillPackageManifest{Version: "1", Skills: skills, Servers: servers}
}

func TestEffectivePackagePersonalSkillWins(t *testing.T) {
	org := manifestOf([]string{"report", "shared"}, model.SkillPackageServers{})
	personal := manifestOf([]string{"shared", "mine"}, model.SkillPackageServers{})
	got := MergeSkillPackages(org, personal)
	want := []EffectiveSkill{{Name: "mine", Source: "personal"}, {Name: "report", Source: "org"}, {Name: "shared", Source: "personal"}}
	if !reflect.DeepEqual(got.Skills, want) {
		t.Fatalf("skills = %+v", got.Skills)
	}
}

func TestEffectivePackageUnionsServers(t *testing.T) {
	org := manifestOf([]string{"a"}, model.SkillPackageServers{
		ToolboxRegistries: []string{"s3://r1/t.json"}, ToolboxServers: []string{"x-mcp"}, AimServers: []string{"slack-mcp"},
		HTTPServers: map[string]string{"docs": "https://a.example"}})
	personal := manifestOf([]string{"b"}, model.SkillPackageServers{
		ToolboxRegistries: []string{"s3://r1/t.json", "s3://r2/t.json"}, ToolboxServers: []string{"y-mcp"}, AimServers: []string{"slack-mcp", "z-mcp"},
		HTTPServers: map[string]string{"more": "https://b.example"}})
	got := MergeSkillPackages(org, personal).Servers
	if !reflect.DeepEqual(got.ToolboxRegistries, []string{"s3://r1/t.json", "s3://r2/t.json"}) ||
		!reflect.DeepEqual(got.ToolboxServers, []string{"x-mcp", "y-mcp"}) ||
		!reflect.DeepEqual(got.AimServers, []string{"slack-mcp", "z-mcp"}) ||
		!reflect.DeepEqual(got.HTTPServers, map[string]string{"docs": "https://a.example", "more": "https://b.example"}) {
		t.Fatalf("servers = %+v", got)
	}
}

func TestEffectivePackageOrgOnly(t *testing.T) {
	got := MergeSkillPackages(manifestOf([]string{"a"}, model.SkillPackageServers{AimServers: []string{"s-mcp"}}), nil)
	if !reflect.DeepEqual(got.Skills, []EffectiveSkill{{Name: "a", Source: "org"}}) || !reflect.DeepEqual(got.Servers.AimServers, []string{"s-mcp"}) {
		t.Fatalf("got %+v", got)
	}
}

func TestEffectivePackagePersonalOnly(t *testing.T) {
	got := MergeSkillPackages(nil, manifestOf([]string{"a"}, model.SkillPackageServers{}))
	if !reflect.DeepEqual(got.Skills, []EffectiveSkill{{Name: "a", Source: "personal"}}) {
		t.Fatalf("got %+v", got)
	}
	empty := MergeSkillPackages(nil, nil)
	if len(empty.Skills) != 0 || empty.Servers.HTTPServers == nil || empty.Servers.AimServers == nil {
		t.Fatalf("empty merge must have empty, non-nil lists: %+v", empty)
	}
}

func TestEffectivePackageHTTPConflictKeepsOrg(t *testing.T) {
	org := manifestOf([]string{"a"}, model.SkillPackageServers{HTTPServers: map[string]string{"docs": "https://org.example"}})
	personal := manifestOf([]string{"b"}, model.SkillPackageServers{HTTPServers: map[string]string{"docs": "https://mine.example"}})
	got := MergeSkillPackages(org, personal)
	if got.Servers.HTTPServers["docs"] != "https://org.example" || !reflect.DeepEqual(got.Conflicts, []string{"docs"}) {
		t.Fatalf("got %+v", got)
	}
}
