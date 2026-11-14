package model

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeUpdateURLContract(t *testing.T) {
	data, err := os.ReadFile("../../../contracts/customer-updates/url-normalization-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Raw        string `json:"raw"`
			Normalized string `json:"normalized"`
			Rejected   string `json:"rejected"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Cases) < 10 {
		t.Fatalf("expected at least 10 cases, got %d", len(fixture.Cases))
	}
	for _, c := range fixture.Cases {
		got, hash, err := NormalizeUpdateURL(c.Raw)
		if c.Rejected != "" {
			if !errors.Is(err, ErrUpdateURLRejected) || !strings.Contains(err.Error(), c.Rejected) {
				t.Errorf("%q: want rejection %q, got err=%v", c.Raw, c.Rejected, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected error %v", c.Raw, err)
			continue
		}
		if got != c.Normalized {
			t.Errorf("%q: got %q want %q", c.Raw, got, c.Normalized)
		}
		if len(hash) != 64 || hash != strings.ToLower(hash) {
			t.Errorf("%q: bad hash %q", c.Raw, hash)
		}
	}
}

func TestNormalizeUpdateURLLengthAndEdges(t *testing.T) {
	long := "https://example.com/" + strings.Repeat("a", MaxUpdateURLLength)
	if _, _, err := NormalizeUpdateURL(long); !errors.Is(err, ErrUpdateURLRejected) || !strings.Contains(err.Error(), "length") {
		t.Errorf("want length rejection, got %v", err)
	}
	for _, raw := range []string{"https://[::1]/x", "https://example/x", "https://", "not a url"} {
		if _, _, err := NormalizeUpdateURL(raw); !errors.Is(err, ErrUpdateURLRejected) {
			t.Errorf("%q: want rejection, got %v", raw, err)
		}
	}
	a, ha, _ := NormalizeUpdateURL("https://example.com/a?utm_x=1")
	b, hb, _ := NormalizeUpdateURL("https://EXAMPLE.com/a")
	if a != b || ha != hb {
		t.Errorf("equivalent URLs differ: %q %q", a, b)
	}
}

func TestInternalHostsMirrorContract(t *testing.T) {
	data, err := os.ReadFile("../../../contracts/customer-updates/internal-hosts-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Suffixes []string `json:"suffixes"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fixture.Suffixes, updateInternalHostSuffixes) {
		t.Fatalf("Go suffix list differs from contract:\n go: %v\n contract: %v", updateInternalHostSuffixes, fixture.Suffixes)
	}
}

func TestUpdateKeyHelpers(t *testing.T) {
	if got := UpdateDraftSK("acc1", "ab"); got != "CUDRAFT#acc1#ab" {
		t.Fatal(got)
	}
	if got := UpdateDecisionSK("ab", "acc1"); got != "CUDEC#ab#acc1" {
		t.Fatal(got)
	}
	if got := UpdateSourceSK("s1"); got != "CUSRC#s1" {
		t.Fatal(got)
	}
	if got := UpdateItemSK("ab"); got != "CUITEM#ab" {
		t.Fatal(got)
	}
	if got := UpdateOptSK("acc1"); got != "CUOPT#acc1" {
		t.Fatal(got)
	}
	if got := UpdateURLPK("ab"); got != "CUURL#ab" {
		t.Fatal(got)
	}
}

func TestNormalizeUpdateURLRejectsIntegerLabelHosts(t *testing.T) {
	for _, raw := range []string{
		"https://0x7f.0x1/x", "https://0177.0.0.0x1/x", "https://0xc0.0xa8.0x0.0x1/x",
		"https://0x7f.1/x", "https://0177.0.0.1/x", "https://0x7f000001.0x1/x",
		"https://99999999999999999999.0x1/x",
	} {
		if _, _, err := NormalizeUpdateURL(raw); !errors.Is(err, ErrUpdateURLRejected) || !strings.Contains(err.Error(), "host") {
			t.Errorf("%q: want host rejection, got %v", raw, err)
		}
	}
	for _, raw := range []string{"https://0x7f.example.com/x", "https://123.example.com/x", "https://a1.0x1.example.org/x"} {
		if _, _, err := NormalizeUpdateURL(raw); err != nil {
			t.Errorf("%q: unexpected rejection %v", raw, err)
		}
	}
}
