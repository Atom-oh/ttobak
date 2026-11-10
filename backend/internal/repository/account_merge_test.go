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

func TestListAccountPartitionKeysIsKeysOnlyPagedAndCapped(t *testing.T) {
	calls := 0
	repo := indexingWire(t, func(target string, body map[string]interface{}) string {
		calls++
		if !strings.HasSuffix(target, ".Query") || body["ConsistentRead"] != true || body["Select"] != nil {
			t.Fatalf("unexpected query: %s %#v", target, body)
		}
		names := wireValues(body)
		if !names["name:SK"] || names["name:title"] || !names["value:CONTACT#"] {
			t.Fatalf("not a keys-only prefix query: %#v", body)
		}
		if calls == 1 {
			return `{"Items":[{"SK":{"S":"CONTACT#a"}},{"SK":{"S":"CONTACT#b"}}],"LastEvaluatedKey":{"PK":{"S":"ACCOUNT#acc"},"SK":{"S":"CONTACT#b"}}}`
		}
		if body["ExclusiveStartKey"] == nil {
			t.Fatal("second page lost its start key")
		}
		return `{"Items":[{"SK":{"S":"CONTACT#c"}}]}`
	})
	keys, over, err := repo.ListAccountPartitionKeys(context.Background(), "acc", "CONTACT#", 3)
	if err != nil || over || len(keys) != 3 || calls != 2 {
		t.Fatalf("keys=%v over=%v err=%v calls=%d", keys, over, err, calls)
	}
	calls = 0
	keys, over, err = repo.ListAccountPartitionKeys(context.Background(), "acc", "CONTACT#", 2)
	if err != nil || !over || len(keys) != 2 {
		t.Fatalf("cap: keys=%v over=%v err=%v", keys, over, err)
	}
	if _, _, err := repo.ListAccountPartitionKeys(context.Background(), "acc", "CONTACT#", 0); err == nil {
		t.Fatal("accepted zero limit")
	}
}
