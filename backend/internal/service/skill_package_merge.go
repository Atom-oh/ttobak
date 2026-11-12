package service

import (
	"sort"

	"github.com/ttobak/backend/internal/model"
)

// EffectiveSkill is one installed skill and the package it comes from.
type EffectiveSkill struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

// EffectivePackage is what [Setup] installs for one user.
type EffectivePackage struct {
	Skills  []EffectiveSkill          `json:"skills"`
	Servers model.SkillPackageServers `json:"servers"`
	// Conflicts names HTTP servers whose personal URL differs from the org
	// URL; the org URL is kept.
	Conflicts []string `json:"conflicts"`
}

// MergeSkillPackages applies the owner's rule: a personal skill replaces the
// org skill of the same name, org skills absent from the personal package
// stay, and server lists are unioned (org order first). Either side may be nil.
func MergeSkillPackages(org, personal *model.SkillPackageManifest) EffectivePackage {
	sources := map[string]string{}
	for _, side := range []struct {
		manifest *model.SkillPackageManifest
		source   string
	}{{org, model.SkillPackageScopeOrg}, {personal, model.SkillPackageScopePersonal}} {
		if side.manifest == nil {
			continue
		}
		for _, skill := range side.manifest.Skills {
			sources[skill] = side.source
		}
	}
	out := EffectivePackage{
		Skills: []EffectiveSkill{},
		Servers: model.SkillPackageServers{
			ToolboxRegistries: []string{}, ToolboxServers: []string{}, AimServers: []string{}, HTTPServers: map[string]string{},
		},
		Conflicts: []string{},
	}
	for name, source := range sources {
		out.Skills = append(out.Skills, EffectiveSkill{Name: name, Source: source})
	}
	sort.Slice(out.Skills, func(i, j int) bool { return out.Skills[i].Name < out.Skills[j].Name })
	for _, manifest := range []*model.SkillPackageManifest{org, personal} {
		if manifest == nil {
			continue
		}
		s := manifest.Servers
		out.Servers.ToolboxRegistries = dedupe(append(out.Servers.ToolboxRegistries, s.ToolboxRegistries...))
		out.Servers.ToolboxServers = dedupe(append(out.Servers.ToolboxServers, s.ToolboxServers...))
		out.Servers.AimServers = dedupe(append(out.Servers.AimServers, s.AimServers...))
		for name, address := range s.HTTPServers {
			if existing, ok := out.Servers.HTTPServers[name]; ok {
				if existing != address {
					out.Conflicts = append(out.Conflicts, name)
				}
				continue
			}
			out.Servers.HTTPServers[name] = address
		}
	}
	sort.Strings(out.Conflicts)
	return out
}
