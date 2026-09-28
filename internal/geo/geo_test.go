package geo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/Hulul-Group/Tusk/internal/config"
)

func cfg(mod func(*config.Config)) *config.Config {
	c := &config.Config{
		GeoProvider:            config.GeoAuto,
		GeoHeader:              "CF-IPCountry",
		GeoTrustCloudflareOnly: true,
		TrustProxy:             true,
	}
	if mod != nil {
		mod(c)
	}
	return c
}

func hdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return h
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"US": "US", "us": "US", "XX": "", "T1": "", "": "", "U": "", "GBR": "GB", " de ": "DE", "1A": "",
	} {
		if got := normalize(in); got != want {
			t.Errorf("normalize(%q)=%q want %q", in, got, want)
		}
	}
}

func TestProviders(t *testing.T) {
	const mmdb = "testdata/GeoIP2-Country-Test.mmdb"
	none, _ := New(cfg(func(c *config.Config) { c.GeoProvider = config.GeoNone }))
	if none.Country(hdr("CF-IPCountry", "US"), "81.2.69.142") != "" || none.Name() != "none" {
		t.Error("none")
	}
	cf, _ := New(cfg(func(c *config.Config) { c.GeoProvider = config.GeoCloudflare }))
	if cf.Country(hdr("CF-IPCountry", "de"), "") != "DE" || cf.Country(hdr("CF-IPCountry", "XX"), "") != "" {
		t.Error("cloudflare")
	}
	custom, _ := New(cfg(func(c *config.Config) { c.GeoProvider = config.GeoCloudflare; c.GeoHeader = "x-country" }))
	if custom.Country(hdr("X-Country", "fr"), "") != "FR" {
		t.Error("custom header")
	}
	g, err := New(cfg(func(c *config.Config) { c.GeoProvider = config.GeoIPFile; c.GeoIPDBPath = mmdb }))
	if err != nil {
		t.Fatal(err)
	}
	if got := g.Country(nil, "81.2.69.142"); got != "GB" {
		t.Errorf("mmdb GB got %q", got)
	}
	if got := g.Country(nil, "2001:218::1"); got != "JP" {
		t.Errorf("mmdb JP got %q", got)
	}
	if g.Country(nil, "10.0.0.1") != "" || g.Country(nil, "junk") != "" {
		t.Error("mmdb unknown")
	}
	if _, err := New(cfg(func(c *config.Config) { c.GeoProvider = config.GeoIPFile; c.GeoIPDBPath = "testdata/missing.mmdb" })); err == nil {
		t.Error("geoip provider with missing file must fail at startup")
	}
	// auto: missing file is NOT an error; header first, then mmdb.
	if _, err := New(cfg(func(c *config.Config) { c.GeoIPDBPath = "testdata/missing.mmdb" })); err != nil {
		t.Error(err)
	}
	auto, _ := New(cfg(func(c *config.Config) { c.GeoIPDBPath = mmdb }))
	if auto.Country(hdr("CF-IPCountry", "SE"), "81.2.69.142") != "SE" {
		t.Error("auto header first")
	}
	if auto.Country(hdr("CF-IPCountry", "XX"), "81.2.69.142") != "GB" {
		t.Error("auto falls back to mmdb")
	}
	if auto.Name() != "auto" {
		t.Error("name")
	}
}

func req(remote string, kv ...string) *http.Request {
	r := httptest.NewRequest("GET", "/x", nil)
	r.RemoteAddr = remote
	r.Header = hdr(kv...)
	return r
}

func TestClientIP(t *testing.T) {
	cip := NewClientIP(cfg(nil))
	cf := "172.64.1.2:443"
	// Direct from Cloudflare edge.
	if got := cip.IP(req(cf, "CF-Connecting-IP", "1.2.3.4", "X-Forwarded-For", "9.9.9.9")); got != "1.2.3.4" {
		t.Errorf("cf direct: %s", got)
	}
	// Spoofed CF-Connecting-IP from a non-CF peer: ignored, XFF left-most used (TRUST_PROXY).
	if got := cip.IP(req("8.8.8.8:1", "CF-Connecting-IP", "1.2.3.4", "X-Forwarded-For", "5.5.5.5, 6.6.6.6")); got != "5.5.5.5" {
		t.Errorf("spoof: %s", got)
	}
	// Behind Traefik (private peer) with CF as the right-most XFF hop.
	r := req("10.0.0.5:5555", "CF-Connecting-IP", "1.2.3.4", "X-Forwarded-For", "1.2.3.4, 2606:4700::1")
	if !cip.FromCloudflare(r) || cip.IP(r) != "1.2.3.4" {
		t.Errorf("behind proxy: %v %s", cip.FromCloudflare(r), cip.IP(r))
	}
	// TRUST_PROXY=false: RemoteAddr.
	nt := NewClientIP(cfg(func(c *config.Config) { c.TrustProxy = false; c.GeoTrustCloudflareOnly = false }))
	if got := nt.IP(req("[2001:db8::1]:80", "X-Forwarded-For", "5.5.5.5")); got != "2001:db8::1" {
		t.Errorf("no proxy: %s", got)
	}
	// Geo header trust.
	if _, ok := cip.GeoHeaders(req("8.8.8.8:1", "CF-IPCountry", "US")).(http.Header); ok {
		t.Error("spoofed geo header must be dropped")
	}
	if cip.GeoHeaders(req(cf, "CF-IPCountry", "US")).Get("CF-IPCountry") != "US" {
		t.Error("cf geo header")
	}
	if nt.GeoHeaders(req("8.8.8.8:1", "CF-IPCountry", "US")).Get("CF-IPCountry") != "US" {
		t.Error("trust-all geo header")
	}
	if !cip.IsCloudflare(netip.MustParseAddr("::ffff:104.16.0.1")) {
		t.Error("v4-mapped")
	}
}

func TestRefresh(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("203.0.113.0/24\n"))
	}))
	defer srv.Close()
	cip := NewClientIP(cfg(func(c *config.Config) { c.CloudflareIPRangesURLs = []string{srv.URL} }))
	if err := cip.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !cip.IsCloudflare(netip.MustParseAddr("203.0.113.9")) || cip.IsCloudflare(netip.MustParseAddr("104.16.0.1")) {
		t.Error("refresh did not replace ranges")
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer bad.Close()
	cip2 := NewClientIP(cfg(func(c *config.Config) { c.CloudflareIPRangesURLs = []string{bad.URL} }))
	if cip2.Refresh(context.Background()) == nil || !cip2.IsCloudflare(netip.MustParseAddr("104.16.0.1")) {
		t.Error("failed refresh must keep embedded list")
	}
}
