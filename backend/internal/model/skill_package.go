package model

// Skill packages (internal-mcp-setup layout zips) that the Mac app's [Setup]
// installs. The organization package is the default for every user; a
// personal package overrides skills of the same name for its owner only.
//
// Org current:  PK SKILLPKG#org, SK CURRENT
// Org history:  PK SKILLPKG#org, SK VERSION#{uploadedAt}#{sha256} (newest 5 kept)
// Personal:     PK USER#{userId}, SK SKILLPKG#personal
//
// Objects: skillpkg/org/{sha256}.zip, skillpkg/user/{userId}/{sha256}.zip and
// uploads in progress at skillpkg/staging/{userId}/{uploadId}.zip.

const (
	SkillPackageOrgPK           = "SKILLPKG#org"
	SkillPackageCurrentSK       = "CURRENT"
	PrefixSkillPackageVersion   = "VERSION#"
	SkillPackagePersonalSK      = "SKILLPKG#personal"
	SkillPackageScopeOrg        = "org"
	SkillPackageScopePersonal   = "personal"
	SkillPackageMaxZipBytes     = 20 << 20
	SkillPackageMaxEntries      = 500
	SkillPackageMaxUncompressed = 50 << 20
	SkillPackageOrgVersionsKept = 5
)

// SkillPackageServers are the MCP server lists of a package. The merge of two
// packages is their union; nothing is ever removed.
type SkillPackageServers struct {
	ToolboxRegistries []string          `json:"toolboxRegistries" dynamodbav:"toolboxRegistries"`
	ToolboxServers    []string          `json:"toolboxServers" dynamodbav:"toolboxServers"`
	AimServers        []string          `json:"aimServers" dynamodbav:"aimServers"`
	HTTPServers       map[string]string `json:"httpServers" dynamodbav:"httpServers"`
}

// SkillPackageManifest is what validation reads from a zip.
type SkillPackageManifest struct {
	Version string              `json:"version"`
	Root    string              `json:"root"`
	Skills  []string            `json:"skills"`
	Servers SkillPackageServers `json:"servers"`
}
