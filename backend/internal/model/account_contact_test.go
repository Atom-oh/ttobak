package model

import "testing"

// referenceSfdcSuffix computes the Salesforce case-checksum suffix of a
// 15-character ID independently of CanonicalSfdcID.
func referenceSfdcSuffix(id string) string {
	out := ""
	for chunk := 0; chunk < 3; chunk++ {
		n := 0
		for i := 4; i >= 0; i-- {
			n *= 2
			if c := id[chunk*5+i]; 'A' <= c && c <= 'Z' {
				n++
			}
		}
		if n < 26 {
			out += string(rune('A' + n))
		} else {
			out += string(rune('0' + n - 26))
		}
	}
	return out
}

func TestCanonicalSfdcID(t *testing.T) {
	// Hand-derived pairs. 001A000001aBcDe: chunk "001A0" has uppercase at
	// index 3 -> 8 -> I; "00001" -> 0 -> A; "aBcDe" has indexes 1 and 3 ->
	// 2+8=10 -> K. 003AB00000abcdE: "003AB" -> 8+16=24 -> Y; "00000" -> A;
	// "abcdE" -> 16 -> Q. 0B0DE0B0DE0B0DE: each chunk -> 2+8+16=26 -> '0'.
	// ABCDEFGHIJKLMNO: each chunk -> 31 -> '5'.
	known := map[string]string{
		"001A000001aBcDe": "001A000001aBcDeIAK",
		"003AB00000abcdE": "003AB00000abcdEYAQ",
		"0B0DE0B0DE0B0DE": "0B0DE0B0DE0B0DE000",
		"ABCDEFGHIJKLMNO": "ABCDEFGHIJKLMNO555",
		"000000000000000": "000000000000000AAA",
	}
	for short, long := range known {
		if got, ok := CanonicalSfdcID(short); !ok || got != long {
			t.Errorf("%s: got %q %v, want %s", short, got, ok, long)
		}
		if got, ok := CanonicalSfdcID(long); !ok || got != long {
			t.Errorf("%s: got %q %v", long, got, ok)
		}
		if short+referenceSfdcSuffix(short) != long {
			t.Errorf("reference disagrees for %s", short)
		}
	}
	for _, short := range []string{"003Hs00000aZbQx", "a0B5g00000XyZw1", "zzzzzZZZZZzZzZz", "5003000000D8cuI"} {
		want := short + referenceSfdcSuffix(short)
		if got, ok := CanonicalSfdcID(short); !ok || got != want {
			t.Errorf("%s: got %q %v, want %s", short, got, ok, want)
		}
		if got, ok := CanonicalSfdcID(want); !ok || got != want {
			t.Errorf("%s: 18-character form rejected", want)
		}
	}
	for _, bad := range []string{
		"", "003AB00000abcd", "003AB00000abcdEY", "003AB00000abcdEYA", "003AB00000abcdEYAQ0",
		"003AB00000abcdEYAq", // suffix is case-sensitive
		"003AB00000abcdeYAQ", // body case changed, suffix stale
		"003AB00000abcdEZZZ", "003AB00000abcd#", "003AB00000abcdé", "003AB00000abcd-YAQ",
	} {
		if got, ok := CanonicalSfdcID(bad); ok || got != "" {
			t.Errorf("%q: accepted as %q", bad, got)
		}
		if ValidSfdcID(bad) {
			t.Errorf("%q: ValidSfdcID accepted", bad)
		}
	}
}
