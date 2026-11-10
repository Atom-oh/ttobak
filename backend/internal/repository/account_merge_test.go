package repository

import (
	"context"
	"strings"
	"testing"
)

func TestAccountPartitionHasPrefixIsSingleConsistentProbe(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		want           bool
	}{
		{"present", `{"Items":[{"PK":{"S":"ACCOUNT#acc"},"SK":{"S":"NEWSSRC#x"}}],"Count":1}`, true},
		{"absent", `{"Items":[],"Count":0}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := indexingWire(t, func(target string, body map[string]interface{}) string {
				if !strings.HasSuffix(target, ".Query") || body["ConsistentRead"] != true || body["Limit"] != float64(1) {
					t.Fatalf("not a one-item consistent query: %s %#v", target, body)
				}
				values := wireValues(body)
				if !values["value:ACCOUNT#acc"] || !values["value:NEWSSRC#"] {
					t.Fatalf("wrong key condition: %#v", body)
				}
				return tc.response
			})
			got, err := repo.AccountPartitionHasPrefix(context.Background(), "acc", "NEWSSRC#")
			if err != nil || got != tc.want {
				t.Fatalf("got %v, %v want %v", got, err, tc.want)
			}
		})
	}
	repo := indexingWire(t, func(string, map[string]interface{}) string { t.Fatal("no call for invalid input"); return "" })
	for _, bad := range [][2]string{{"", "KBREF#"}, {"a#b", "KBREF#"}, {"acc", ""}} {
		if _, err := repo.AccountPartitionHasPrefix(context.Background(), bad[0], bad[1]); err == nil {
			t.Fatalf("accepted %v", bad)
		}
	}
}
