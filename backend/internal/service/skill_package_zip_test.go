package service

import (
	"archive/zip"
	"bytes"
	"errors"
	"io/fs"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

type zipEntry struct {
	name string
	data string
	mode fs.FileMode
	// declared overrides the uncompressed size written to the header.
	declared uint64
}

func buildZip(t *testing.T, entries []zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, e := range entries {
		header := &zip.FileHeader{Name: e.name, Method: zip.Store}
		if e.mode != 0 {
			header.SetMode(e.mode)
		}
		f, err := w.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(e.data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const demoScript = `#!/usr/bin/env bash
VERSION="9.9.9"
TOOLBOX_REGISTRIES=(
  "s3://example-registry-one/tools.json"
  # a comment line
  "s3://example-registry-two/tools.json"
)
TOOLBOX_MCPS_BASE=(alpha-mcp beta-mcp)
TOOLBOX_MCPS_EXTRA=(extra-mcp)
AIM_MCPS_BASE=(gamma-mcp delta-mcp)
AIM_MCPS_EXTRA=(
  never-mcp other-mcp
)
HTTP_MCPS=("docs-mcp=https://docs.example.com/mcp")
TOOLBOX_MCPS=("${TOOLBOX_MCPS_BASE[@]}")
echo "$HOME"
`

func demoPackage(extra ...zipEntry) []zipEntry {
	return append([]zipEntry{
		{name: "pkg-v9.9.9/README.md", data: "readme"},
		{name: "pkg-v9.9.9/setup-internal-mcp.sh", data: demoScript},
		{name: "pkg-v9.9.9/skills/demo-skill/SKILL.md", data: "# demo"},
		{name: "pkg-v9.9.9/skills/demo-skill/scripts/run.py", data: "print(1)"},
	}, extra...)
}

func ruleOf(t *testing.T, err error) string {
	t.Helper()
	var pkgErr *SkillPackageError
	if !errors.As(err, &pkgErr) {
		t.Fatalf("want SkillPackageError, got %v", err)
	}
	return pkgErr.Rule
}

func TestValidateSkillPackageAcceptsInstallerLayout(t *testing.T) {
	manifest, err := ValidateSkillPackage(buildZip(t, demoPackage()))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != "9.9.9" || manifest.Root != "pkg-v9.9.9" {
		t.Fatalf("version/root = %q %q", manifest.Version, manifest.Root)
	}
	if !reflect.DeepEqual(manifest.Skills, []string{"demo-skill"}) {
		t.Fatalf("skills = %v", manifest.Skills)
	}
	s := manifest.Servers
	if !reflect.DeepEqual(s.ToolboxRegistries, []string{"s3://example-registry-one/tools.json", "s3://example-registry-two/tools.json"}) {
		t.Fatalf("registries = %v", s.ToolboxRegistries)
	}
	if !reflect.DeepEqual(s.ToolboxServers, []string{"alpha-mcp", "beta-mcp"}) || !reflect.DeepEqual(s.AimServers, []string{"gamma-mcp", "delta-mcp"}) {
		t.Fatalf("servers = %v %v", s.ToolboxServers, s.AimServers)
	}
	if !reflect.DeepEqual(s.HTTPServers, map[string]string{"docs-mcp": "https://docs.example.com/mcp"}) {
		t.Fatalf("http = %v", s.HTTPServers)
	}
}

func TestValidateSkillPackageIgnoresExtraArrays(t *testing.T) {
	manifest, err := ValidateSkillPackage(buildZip(t, demoPackage()))
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(append(manifest.Servers.ToolboxServers, manifest.Servers.AimServers...), ",")
	for _, name := range []string{"extra-mcp", "never-mcp", "other-mcp"} {
		if strings.Contains(all, name) {
			t.Fatalf("%s from an *_EXTRA array was read", name)
		}
	}
}

func TestValidateSkillPackagePrefersManifest(t *testing.T) {
	manifest := `{"version":"2.0.0","toolboxRegistries":[],"toolboxServers":["only-mcp"],"aimServers":[],"httpServers":{}}`
	got, err := ValidateSkillPackage(buildZip(t, demoPackage(zipEntry{name: "pkg-v9.9.9/ttobak-manifest.json", data: manifest})))
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "2.0.0" || !reflect.DeepEqual(got.Servers.ToolboxServers, []string{"only-mcp"}) || len(got.Servers.AimServers) != 0 {
		t.Fatalf("manifest not preferred: %+v", got)
	}
}

func TestValidateSkillPackageVersionFallsBackToFolder(t *testing.T) {
	got, err := ValidateSkillPackage(buildZip(t, []zipEntry{{name: "pkg-v1.2.3/skills/a-skill/SKILL.md", data: "x"}}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "pkg-v1.2.3" || len(got.Servers.ToolboxServers) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestValidateSkillPackageRejectsUnsafeEntries(t *testing.T) {
	cases := map[string]struct {
		entries []zipEntry
		rule    string
	}{
		"parent":      {demoPackage(zipEntry{name: "pkg-v9.9.9/skills/demo-skill/../../x", data: "x"}), RuleUnsafePath},
		"absolute":    {demoPackage(zipEntry{name: "/abs/x", data: "x"}), RuleUnsafePath},
		"backslash":   {demoPackage(zipEntry{name: `pkg-v9.9.9\skills\x`, data: "x"}), RuleUnsafePath},
		"symlink":     {demoPackage(zipEntry{name: "pkg-v9.9.9/skills/demo-skill/link", data: "/etc/passwd", mode: fs.ModeSymlink | 0o777}), RuleNotRegularFile},
		"two roots":   {demoPackage(zipEntry{name: "other/skills/x/SKILL.md", data: "x"}), RuleTopLevel},
		"root file":   {demoPackage(zipEntry{name: "loose.txt", data: "x"}), RuleTopLevel},
		"no skills":   {[]zipEntry{{name: "pkg/README.md", data: "x"}}, RuleNoSkills},
		"no SKILL.md": {demoPackage(zipEntry{name: "pkg-v9.9.9/skills/empty-skill/notes.md", data: "x"}), RuleSkillMissingMD},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ValidateSkillPackage(buildZip(t, c.entries))
			if got := ruleOf(t, err); got != c.rule {
				t.Fatalf("rule = %s, want %s (%v)", got, c.rule, err)
			}
		})
	}
}

func TestValidateSkillPackageLimits(t *testing.T) {
	many := demoPackage()
	for i := 0; len(many) <= 500; i++ {
		many = append(many, zipEntry{name: "pkg-v9.9.9/skills/demo-skill/f" + strconv.Itoa(i), data: ""})
	}
	if got := ruleOf(t, func() error { _, err := ValidateSkillPackage(buildZip(t, many)); return err }()); got != RuleTooManyEntries {
		t.Fatalf("rule = %s", got)
	}
	big := make([]byte, 20<<20+1)
	if got := ruleOf(t, func() error { _, err := ValidateSkillPackage(big); return err }()); got != RuleZipTooLarge {
		t.Fatalf("rule = %s", got)
	}
	if got := uncompressedRule(t); got != RuleUncompressedTooLarge {
		t.Fatalf("rule = %s", got)
	}
}

// uncompressedRule validates a zip whose declared sizes sum over 50 MiB.
func uncompressedRule(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, name := range []string{"pkg/skills/a-skill/SKILL.md", "pkg/skills/a-skill/big.bin"} {
		f, err := w.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			t.Fatal(err)
		}
		size := 1
		if strings.HasSuffix(name, ".bin") {
			size = 51 << 20
		}
		if _, err := f.Write(make([]byte, size)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := ValidateSkillPackage(buf.Bytes())
	return ruleOf(t, err)
}

func TestValidateSkillPackageNames(t *testing.T) {
	script := func(body string) zipEntry {
		return zipEntry{name: "pkg-v9.9.9/setup-internal-mcp.sh", data: body}
	}
	base := []zipEntry{{name: "pkg-v9.9.9/skills/demo-skill/SKILL.md", data: "x"}}
	cases := map[string]struct {
		entries []zipEntry
		rule    string
	}{
		"skill name":   {[]zipEntry{{name: "pkg/skills/Bad_Name/SKILL.md", data: "x"}}, RuleBadSkillName},
		"server name":  {append(base, script("AIM_MCPS_BASE=(Bad.Name)")), RuleBadServerName},
		"http url":     {append(base, script(`HTTP_MCPS=("docs=http://docs.example.com")`)), RuleBadHTTPURL},
		"registry":     {append(base, script(`TOOLBOX_REGISTRIES=("https://example.com/tools.json")`)), RuleBadRegistry},
		"expansion":    {append(base, script(`AIM_MCPS_BASE=("$HOME")`)), RuleBadServerList},
		"backtick":     {append(base, script("AIM_MCPS_BASE=(`id`)")), RuleBadServerList},
		"unterminated": {append(base, script("AIM_MCPS_BASE=(alpha-mcp")), RuleBadServerList},
		"bad manifest": {append(base, zipEntry{name: "pkg-v9.9.9/ttobak-manifest.json", data: "{"}), RuleBadManifest},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ValidateSkillPackage(buildZip(t, c.entries))
			if got := ruleOf(t, err); got != c.rule {
				t.Fatalf("rule = %s, want %s (%v)", got, c.rule, err)
			}
		})
	}
}

func TestValidateSkillPackageReportLayout(t *testing.T) {
	entries := demoPackage(
		zipEntry{name: "pkg-v9.9.9/skills/sa-performance-report/SKILL.md", data: "x"},
		zipEntry{name: "pkg-v9.9.9/skills/sa-performance-report/scripts/calendar_count.py", data: "x"},
	)
	_, err := ValidateSkillPackage(buildZip(t, entries))
	if got := ruleOf(t, err); got != RuleReportSkillLayout {
		t.Fatalf("rule = %s", got)
	}
	if !strings.Contains(err.Error(), "references/report-structure.md") {
		t.Fatalf("detail does not name the missing file: %v", err)
	}
	entries = append(entries, zipEntry{name: "pkg-v9.9.9/skills/sa-performance-report/references/report-structure.md", data: "x"})
	if _, err := ValidateSkillPackage(buildZip(t, entries)); err != nil {
		t.Fatal(err)
	}
}
