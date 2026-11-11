package service

import (
	"slices"
	"testing"
)

func TestMatchTechTags(t *testing.T) {
	ids, other := matchTechTags([]string{"EKS", " k8s ", "oracle-db", "foo", "kubernetes", "Foo", "general", ""})
	if !slices.Equal(ids, []string{"eks", "oracle"}) {
		t.Fatalf("ids = %v", ids)
	}
	if !slices.Equal(other, []string{"foo"}) {
		t.Fatalf("other = %v", other)
	}
	if ids, other := matchTechTags(nil); ids != nil || other != nil {
		t.Fatalf("empty input = %v %v", ids, other)
	}
}

func TestTechCatalogAliasesAreUniqueAndLowercase(t *testing.T) {
	seen := map[string]string{}
	for _, entry := range techCatalog {
		if _, ok := techByID(entry.ID); !ok {
			t.Fatalf("techByID(%q) missing", entry.ID)
		}
		for _, alias := range entry.Aliases {
			if owner, dup := seen[alias]; dup {
				t.Fatalf("alias %q in %s and %s", alias, owner, entry.ID)
			}
			if alias != normalizeTechAlias(alias) {
				t.Fatalf("alias %q is not normalized", alias)
			}
			seen[alias] = entry.ID
		}
	}
}
