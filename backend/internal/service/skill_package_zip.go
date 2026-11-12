package service

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/ttobak/backend/internal/model"
)

// Validation rules of a skill package zip. The web UI maps each rule to Korean
// copy; the Mac adapter (mcp-server/src/setup/package.ts) applies the same
// rules again before installing. A package is never extracted or executed here.
const (
	RuleNotZip               = "not-zip"
	RuleZipTooLarge          = "zip-too-large"
	RuleTooManyEntries       = "too-many-entries"
	RuleUncompressedTooLarge = "uncompressed-too-large"
	RuleUnsafePath           = "unsafe-path"
	RuleNotRegularFile       = "not-regular-file"
	RuleTopLevel             = "top-level"
	RuleNoSkills             = "no-skills"
	RuleSkillMissingMD       = "skill-missing-md"
	RuleBadSkillName         = "bad-skill-name"
	RuleBadServerName        = "bad-server-name"
	RuleBadRegistry          = "bad-registry"
	RuleBadHTTPURL           = "bad-http-url"
	RuleReportSkillLayout    = "report-skill-layout"
	RuleBadManifest          = "bad-manifest"
	RuleBadServerList        = "bad-server-list"
)

// SkillPackageError names the failed rule; Detail is bounded, printable text.
type SkillPackageError struct {
	Rule   string
	Detail string
}

func (e *SkillPackageError) Error() string {
	if e.Detail == "" {
		return e.Rule
	}
	return e.Rule + ": " + e.Detail
}

func pkgErr(rule, detail string) error {
	return &SkillPackageError{Rule: rule, Detail: printableDetail(detail)}
}

func printableDetail(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	if r := []rune(s); len(r) > 120 {
		s = string(r[:120])
	}
	return s
}

const (
	skillPackageScriptName   = "setup-internal-mcp.sh"
	skillPackageManifestName = "ttobak-manifest.json"
	skillPackageTextMaxBytes = 1 << 20
	skillPackageMaxServers   = 100
	reportSkillName          = "sa-performance-report"
)

// reportSkillLayout mirrors SKILL_LAYOUT in mcp-server/src/report/claude.ts:
// the files a report run requires of sa-performance-report.
var reportSkillLayout = []string{"scripts/calendar_count.py", "references/report-structure.md"}

var (
	skillPackageNamePattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	skillPackageRegistryPattern = regexp.MustCompile(`^s3://[a-z0-9][a-z0-9.-]{2,62}/[A-Za-z0-9._/-]{1,512}$`)
	skillPackageVersionPattern  = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}$`)
	scriptVersionPattern        = regexp.MustCompile(`(?m)^VERSION="([^"\n]*)"[ \t]*$`)
	bareTokenPattern            = regexp.MustCompile(`^[A-Za-z0-9._:/=@+-]+$`)
)

// The installer's base arrays; the *_EXTRA (--all) arrays are never read.
var scriptArrays = []string{"TOOLBOX_REGISTRIES", "TOOLBOX_MCPS_BASE", "AIM_MCPS_BASE", "HTTP_MCPS"}

// ValidateSkillPackage checks a package zip and returns its skills and
// server lists. It reads only the optional manifest and the installer script
// (as text) and checks every entry name; nothing is extracted.
func ValidateSkillPackage(data []byte) (*model.SkillPackageManifest, error) {
	if len(data) > model.SkillPackageMaxZipBytes {
		return nil, pkgErr(RuleZipTooLarge, "")
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, pkgErr(RuleNotZip, "")
	}
	if len(reader.File) > model.SkillPackageMaxEntries {
		return nil, pkgErr(RuleTooManyEntries, "")
	}
	var total uint64
	root := ""
	files := map[string]*zip.File{}
	skillFiles := map[string]map[string]bool{}
	for _, f := range reader.File {
		total += f.UncompressedSize64
		if total > model.SkillPackageMaxUncompressed {
			return nil, pkgErr(RuleUncompressedTooLarge, "")
		}
		name, isDir, err := entryName(f)
		if err != nil {
			return nil, err
		}
		parts := strings.Split(name, "/")
		if root == "" {
			root = parts[0]
		}
		if parts[0] != root || (len(parts) == 1 && !isDir) {
			return nil, pkgErr(RuleTopLevel, name)
		}
		if isDir {
			continue
		}
		files[name] = f
		if len(parts) >= 4 && parts[1] == "skills" {
			skill := parts[2]
			if skillFiles[skill] == nil {
				skillFiles[skill] = map[string]bool{}
			}
			skillFiles[skill][strings.Join(parts[3:], "/")] = true
		} else if len(parts) == 3 && parts[1] == "skills" {
			return nil, pkgErr(RuleBadSkillName, parts[2])
		}
	}
	if root == "" || len(skillFiles) == 0 {
		return nil, pkgErr(RuleNoSkills, "")
	}
	skills := make([]string, 0, len(skillFiles))
	for skill, inside := range skillFiles {
		if !skillPackageNamePattern.MatchString(skill) {
			return nil, pkgErr(RuleBadSkillName, skill)
		}
		if !inside["SKILL.md"] {
			return nil, pkgErr(RuleSkillMissingMD, skill)
		}
		if skill == reportSkillName {
			for _, required := range reportSkillLayout {
				if !inside[required] {
					return nil, pkgErr(RuleReportSkillLayout, required)
				}
			}
		}
		skills = append(skills, skill)
	}
	sort.Strings(skills)

	manifest := &model.SkillPackageManifest{Root: root, Skills: skills}
	if f := files[root+"/"+skillPackageManifestName]; f != nil {
		text, err := readEntry(f)
		if err != nil {
			return nil, pkgErr(RuleBadManifest, "")
		}
		if err := parseManifest(text, manifest); err != nil {
			return nil, err
		}
	} else if f := files[root+"/"+skillPackageScriptName]; f != nil {
		text, err := readEntry(f)
		if err != nil {
			return nil, pkgErr(RuleBadServerList, "")
		}
		if err := parseScript(string(text), manifest); err != nil {
			return nil, err
		}
	}
	if manifest.Version == "" {
		manifest.Version = root
	}
	if manifest.Servers.HTTPServers == nil {
		manifest.Servers.HTTPServers = map[string]string{}
	}
	manifest.Servers.ToolboxRegistries = nonNil(manifest.Servers.ToolboxRegistries)
	manifest.Servers.ToolboxServers = nonNil(manifest.Servers.ToolboxServers)
	manifest.Servers.AimServers = nonNil(manifest.Servers.AimServers)
	if err := checkServers(manifest.Servers); err != nil {
		return nil, err
	}
	return manifest, nil
}

func nonNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

// entryName rejects absolute, backslash, dot-segment and empty-segment names
// and anything that is neither a regular file nor a directory.
func entryName(f *zip.File) (string, bool, error) {
	name := f.Name
	mode := f.Mode()
	isDir := mode.IsDir() || strings.HasSuffix(name, "/")
	if isDir {
		name = strings.TrimSuffix(name, "/")
	}
	if name == "" || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\\x00") || path.Clean(name) != name {
		return "", false, pkgErr(RuleUnsafePath, f.Name)
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return "", false, pkgErr(RuleUnsafePath, f.Name)
		}
	}
	if !isDir && !mode.IsRegular() {
		return "", false, pkgErr(RuleNotRegularFile, f.Name)
	}
	return name, isDir, nil
}

// readEntry reads at most 1 MiB and fails when the entry is larger or its
// real size differs from the declared one.
func readEntry(f *zip.File) ([]byte, error) {
	if f.UncompressedSize64 > skillPackageTextMaxBytes {
		return nil, fmt.Errorf("too large")
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, skillPackageTextMaxBytes+1))
	if err != nil || uint64(len(data)) != f.UncompressedSize64 {
		return nil, fmt.Errorf("size mismatch")
	}
	return data, nil
}

func parseManifest(text []byte, m *model.SkillPackageManifest) error {
	var raw struct {
		Version           string            `json:"version"`
		ToolboxRegistries []string          `json:"toolboxRegistries"`
		ToolboxServers    []string          `json:"toolboxServers"`
		AimServers        []string          `json:"aimServers"`
		HTTPServers       map[string]string `json:"httpServers"`
	}
	decoder := json.NewDecoder(bytes.NewReader(text))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil || decoder.More() {
		return pkgErr(RuleBadManifest, "")
	}
	if raw.Version != "" && !skillPackageVersionPattern.MatchString(raw.Version) {
		return pkgErr(RuleBadManifest, "version")
	}
	m.Version = raw.Version
	m.Servers = model.SkillPackageServers{
		ToolboxRegistries: dedupe(raw.ToolboxRegistries),
		ToolboxServers:    dedupe(raw.ToolboxServers),
		AimServers:        dedupe(raw.AimServers),
		HTTPServers:       raw.HTTPServers,
	}
	return nil
}

// parseScript reads the installer's literal base arrays. Tokens are quoted
// strings without expansion characters or bare words; `#` starts a comment.
func parseScript(text string, m *model.SkillPackageManifest) error {
	if match := scriptVersionPattern.FindStringSubmatch(text); match != nil && skillPackageVersionPattern.MatchString(match[1]) {
		m.Version = match[1]
	}
	values := map[string][]string{}
	for _, name := range scriptArrays {
		pattern := regexp.MustCompile(`(?m)^` + name + `=\(`)
		locations := pattern.FindAllStringIndex(text, -1)
		if len(locations) > 1 {
			return pkgErr(RuleBadServerList, name)
		}
		if len(locations) == 0 {
			continue
		}
		tokens, err := arrayTokens(text[locations[0][1]:])
		if err != nil {
			return pkgErr(RuleBadServerList, name)
		}
		values[name] = tokens
	}
	m.Servers = model.SkillPackageServers{
		ToolboxRegistries: dedupe(values["TOOLBOX_REGISTRIES"]),
		ToolboxServers:    dedupe(values["TOOLBOX_MCPS_BASE"]),
		AimServers:        dedupe(values["AIM_MCPS_BASE"]),
		HTTPServers:       map[string]string{},
	}
	for _, pair := range values["HTTP_MCPS"] {
		name, address, ok := strings.Cut(pair, "=")
		if !ok {
			return pkgErr(RuleBadServerList, "HTTP_MCPS")
		}
		m.Servers.HTTPServers[name] = address
	}
	return nil
}

func arrayTokens(rest string) ([]string, error) {
	var tokens []string
	for i := 0; i < len(rest); {
		switch c := rest[i]; {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == ')':
			return tokens, nil
		case c == '#':
			end := strings.IndexByte(rest[i:], '\n')
			if end < 0 {
				return nil, fmt.Errorf("unterminated")
			}
			i += end
		case c == '"':
			end := strings.IndexByte(rest[i+1:], '"')
			if end < 0 {
				return nil, fmt.Errorf("unterminated quote")
			}
			token := rest[i+1 : i+1+end]
			if token == "" || strings.ContainsAny(token, "$`\\\n") || !bareTokenPattern.MatchString(token) {
				return nil, fmt.Errorf("token")
			}
			tokens = append(tokens, token)
			i += end + 2
		default:
			end := strings.IndexAny(rest[i:], " \t\r\n)")
			if end < 0 {
				return nil, fmt.Errorf("unterminated")
			}
			token := rest[i : i+end]
			if !bareTokenPattern.MatchString(token) {
				return nil, fmt.Errorf("token")
			}
			tokens = append(tokens, token)
			i += end
		}
		if len(tokens) > skillPackageMaxServers {
			return nil, fmt.Errorf("too many")
		}
	}
	return nil, fmt.Errorf("unterminated")
}

func dedupe(list []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, item := range list {
		if !seen[item] {
			seen[item] = true
			out = append(out, item)
		}
	}
	return out
}

func checkServers(s model.SkillPackageServers) error {
	if len(s.ToolboxRegistries) > skillPackageMaxServers || len(s.ToolboxServers) > skillPackageMaxServers ||
		len(s.AimServers) > skillPackageMaxServers || len(s.HTTPServers) > skillPackageMaxServers {
		return pkgErr(RuleBadServerList, "too many entries")
	}
	for _, registry := range s.ToolboxRegistries {
		if !skillPackageRegistryPattern.MatchString(registry) {
			return pkgErr(RuleBadRegistry, registry)
		}
	}
	for _, name := range append(append([]string{}, s.ToolboxServers...), s.AimServers...) {
		if !skillPackageNamePattern.MatchString(name) {
			return pkgErr(RuleBadServerName, name)
		}
	}
	for name, address := range s.HTTPServers {
		if !skillPackageNamePattern.MatchString(name) {
			return pkgErr(RuleBadServerName, name)
		}
		if !validHTTPServerURL(address) {
			return pkgErr(RuleBadHTTPURL, name)
		}
	}
	return nil
}

func validHTTPServerURL(address string) bool {
	if len(address) > 512 {
		return false
	}
	u, err := url.Parse(address)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.Fragment == "" &&
		!strings.ContainsAny(address, " \t\r\n\"'`$\\")
}
