package uaparse

import (
	"encoding/json"
	"os"
	"testing"
)

type golden struct {
	UA      string `json:"ua"`
	Browser string `json:"browser"`
	OS      string `json:"os"`
	Bot     bool   `json:"bot"`
}

// testdata/ua_golden.json is produced by testdata/gen_ua_golden.js running the real
// express-useragent 2.2.1 + isbot 5.1.19 + Kutt's bucket functions under Node 22
// over ~3.2k UAs (isbot fixtures, monperrus/crawler-user-agents, hand-written edge cases).
func loadGolden(t testing.TB) []golden {
	b, err := os.ReadFile("testdata/ua_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var g []golden
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

func TestGoldenAgainstNode(t *testing.T) {
	g := loadGolden(t)
	var bad int
	for _, c := range g {
		b, o := Parse(c.UA)
		if b != c.Browser || o != c.OS || Browser(c.UA) != c.Browser || OS(c.UA) != c.OS {
			bad++
			if bad < 30 {
				t.Errorf("ua=%q got (%s,%s) want (%s,%s)", c.UA, b, o, c.Browser, c.OS)
			}
		}
		if got := IsBot(c.UA); got != c.Bot {
			bad++
			if bad < 30 {
				t.Errorf("isbot(%q)=%v want %v", c.UA, got, c.Bot)
			}
		}
	}
	if bad > 0 {
		t.Fatalf("%d mismatches out of %d", bad, len(g))
	}
	t.Logf("%d UAs match Node exactly", len(g))
}

func TestKnown(t *testing.T) {
	cases := []struct{ ua, br, os string }{
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36 Edg/120.0.0.0", "edge", "windows"},
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.1 Safari/605.1.15", "other", "macos"},
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 17_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.1 Mobile/15E148 Safari/604.1", "other", "ios"},
		{"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Mobile Safari/537.36", "chrome", "android"},
		{"Mozilla/5.0 (Windows NT 6.1; Trident/7.0; rv:11.0) like Gecko", "ie", "windows"},
	}
	for _, c := range cases {
		if b, o := Parse(c.ua); b != c.br || o != c.os {
			t.Errorf("%q: got %s/%s want %s/%s", c.ua, b, o, c.br, c.os)
		}
	}
	if IsBot("") {
		t.Error("isbot v5: empty UA is not a bot (Boolean(ua) && ...)")
	}
	if !IsBot("Googlebot/2.1 (+http://www.google.com/bot.html)") || IsBot(cases[0].ua) {
		t.Error("basic isbot")
	}
}

func TestZeroAlloc(t *testing.T) {
	ua := "Mozilla/5.0 (iPhone; CPU iPhone OS 17_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.1 Mobile/15E148 Safari/604.1"
	if n := testing.AllocsPerRun(100, func() { Parse(ua) }); n != 0 {
		t.Fatalf("Parse allocs=%v", n)
	}
}

var sinkS string
var sinkB bool

const benchUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

func BenchmarkBrowser(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		sinkS = Browser(benchUA)
	}
}

func BenchmarkOS(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		sinkS = OS(benchUA)
	}
}

func BenchmarkParse(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		sinkS, _ = Parse(benchUA)
	}
}

func BenchmarkIsBot(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		sinkB = IsBot(benchUA)
	}
}

func BenchmarkIsBotCrawler(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		sinkB = IsBot("Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)")
	}
}
