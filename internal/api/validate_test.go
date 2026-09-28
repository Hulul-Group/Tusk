package api

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"
)

// Vectors produced by Kutt's own dependencies:
//
//	testdata/node_validators.json  <- node vvectors.js (validator 13, node:url, utils.urlRegex)
//	../auth/testdata/node_vectors.json "ms"/"msLong" <- ms 2.1.3
func TestNodeValidatorVectors(t *testing.T) {
	b, err := os.ReadFile("testdata/node_validators.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		URL   map[string]bool `json:"url"`
		Email map[string]bool `json:"email"`
		Host  map[string]struct {
			Host     *string `json:"host"`
			Hostname *string `json:"hostname"`
		} `json:"host"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	for in, want := range v.URL {
		if got := validTarget(in); got != want {
			t.Errorf("validTarget(%q)=%v node=%v", in, got, want)
		}
	}
	for in, want := range v.Email {
		if got := isEmail(in); got != want {
			t.Errorf("isEmail(%q)=%v node=%v", in, got, want)
		}
	}
	for in, want := range v.Host {
		got := parseLegacyURL(in)
		wh, wn := "", ""
		if want.Host != nil {
			wh = *want.Host
		}
		if want.Hostname != nil {
			wn = *want.Hostname
		}
		if got.Host != wh || got.Hostname != wn {
			t.Errorf("url.parse(%q) host=%q hostname=%q node=%q/%q", in, got.Host, got.Hostname, wh, wn)
		}
	}
}

func TestMSVectors(t *testing.T) {
	b, err := os.ReadFile("../auth/testdata/node_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		MS     map[string]*float64 `json:"ms"`
		MSLong map[string]string   `json:"msLong"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	for in, want := range v.MS {
		got, ok := parseMS(in)
		if want == nil {
			if ok {
				t.Errorf("ms(%q)=%v, node undefined", in, got)
			}
			continue
		}
		if !ok || got != *want {
			t.Errorf("ms(%q)=%v,%v node=%v", in, got, ok, *want)
		}
	}
	for in, want := range v.MSLong {
		n, _ := strconv.ParseFloat(in, 64)
		if got := fmtMSLong(n); got != want {
			t.Errorf("ms(%s,{long})=%q node=%q", in, got, want)
		}
	}
}

func TestHelpersJS(t *testing.T) {
	cases := map[string]string{"0": "0", "999": "999", "1000": "1,000", "1234567": "1,234,567", "-1234": "-1,234"}
	for in, want := range cases {
		n, _ := strconv.ParseInt(in, 10, 64)
		if got := localeInt(n); got != want {
			t.Errorf("localeInt(%s)=%s", in, got)
		}
	}
	for in, want := range map[string]int64{"10": 10, " 7x": 7, "-3": -3, "+4": 4} {
		if got, ok := parseIntJS(in); !ok || got != want {
			t.Errorf("parseInt(%q)=%d", in, got)
		}
	}
	if _, ok := parseIntJS("x1"); ok {
		t.Error("parseInt(x1) should be NaN")
	}
	if a := nanoid("ab", 30); len(a) != 30 {
		t.Error("nanoid length")
	}
	for i := 0; i < 100; i++ {
		for _, c := range nanoid("abcdefghkmnpqrstuvwxyzABCDEFGHKLMNPQRSTUVWXYZ23456789", 6) {
			if !newCustomAlphabetRe("abcdefghkmnpqrstuvwxyzABCDEFGHKLMNPQRSTUVWXYZ23456789").MatchString(string(c)) {
				t.Fatalf("char %q outside alphabet", c)
			}
		}
	}
}
