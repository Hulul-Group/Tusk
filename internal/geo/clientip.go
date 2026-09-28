package geo

import (
	"bufio"
	"context"
	_ "embed"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Hulul-Group/Tusk/internal/config"
)

// Cloudflare's published ranges, fetched from https://www.cloudflare.com/ips-v4 and
// /ips-v6 on 2026-09-28. Refreshed at runtime every 24h from CLOUDFLARE_IP_RANGES_URLS.
//
//go:embed cloudflare_ips.txt
var embeddedCloudflareRanges string

// ClientIP extracts the trusted client IP from a request.
//
// Order (docs/srs/00-overview.md GEO-005, ARCHITECTURE §4):
//  1. GEO_TRUST_CLOUDFLARE_ONLY and the peer is a Cloudflare edge -> CF-Connecting-IP.
//     With TRUST_PROXY the "peer" is the first untrusted hop: the right-most
//     X-Forwarded-For entry appended by our own reverse proxy (Traefik) is checked
//     when the TCP peer itself is private/loopback.
//  2. TRUST_PROXY -> left-most X-Forwarded-For (Express `trust proxy = true`: req.ip is the
//     left-most entry, server.js `app.set("trust proxy", true)` when TRUST_PROXY).
//  3. RemoteAddr.
type ClientIP struct {
	trustProxy   bool
	cfOnly       bool
	urls         []string
	ranges       atomic.Pointer[[]netip.Prefix]
	httpClient   *http.Client
	refreshEvery time.Duration
}

// NewClientIP builds the extractor with the embedded Cloudflare list.
func NewClientIP(cfg *config.Config) *ClientIP {
	c := &ClientIP{
		trustProxy:   cfg.TrustProxy,
		cfOnly:       cfg.GeoTrustCloudflareOnly,
		urls:         cfg.CloudflareIPRangesURLs,
		httpClient:   &http.Client{Timeout: 15 * time.Second},
		refreshEvery: 24 * time.Hour,
	}
	p, _ := parseRanges(strings.NewReader(embeddedCloudflareRanges))
	c.ranges.Store(&p)
	return c
}

// Run refreshes the Cloudflare ranges every 24h until ctx is done. Failures keep the
// previous list. Call it in a goroutine; it is optional (the embedded list works offline).
func (c *ClientIP) Run(ctx context.Context) {
	t := time.NewTicker(c.refreshEvery)
	defer t.Stop()
	c.Refresh(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.Refresh(ctx)
		}
	}
}

// Refresh downloads all configured URLs; the list is replaced only if every URL succeeded
// and the result is non-empty.
func (c *ClientIP) Refresh(ctx context.Context) error {
	if len(c.urls) == 0 {
		return nil
	}
	var all []netip.Prefix
	for _, u := range c.urls {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			slog.Warn("cloudflare ip ranges refresh failed", "url", u, "err", err)
			return err
		}
		p, err := parseRanges(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK || len(p) == 0 {
			slog.Warn("cloudflare ip ranges refresh: bad response", "url", u, "status", resp.StatusCode, "err", err)
			return errBadRanges
		}
		all = append(all, p...)
	}
	c.ranges.Store(&all)
	return nil
}

// SetRanges replaces the Cloudflare ranges (tests).
func (c *ClientIP) SetRanges(p []netip.Prefix) { c.ranges.Store(&p) }

type geoErr string

func (e geoErr) Error() string { return string(e) }

const errBadRanges = geoErr("geo: invalid cloudflare ranges response")

func parseRanges(r io.Reader) ([]netip.Prefix, error) {
	var out []netip.Prefix
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p, err := netip.ParsePrefix(line)
		if err != nil {
			return nil, err
		}
		out = append(out, p.Masked())
	}
	return out, sc.Err()
}

// IsCloudflare reports whether ip is inside Cloudflare's ranges.
func (c *ClientIP) IsCloudflare(ip netip.Addr) bool {
	ip = ip.Unmap()
	for _, p := range *c.ranges.Load() {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// peer returns the address of the hop that talked to our edge: the TCP peer, or, when
// TRUST_PROXY and the TCP peer is a private/loopback proxy, the right-most XFF entry.
func (c *ClientIP) peer(r *http.Request) (netip.Addr, bool) {
	remote, ok := parseHostPort(r.RemoteAddr)
	if !ok {
		return netip.Addr{}, false
	}
	if c.trustProxy && (remote.IsPrivate() || remote.IsLoopback()) {
		if xff := r.Header.Values("X-Forwarded-For"); len(xff) > 0 {
			last := xff[len(xff)-1]
			if i := strings.LastIndexByte(last, ','); i >= 0 {
				last = last[i+1:]
			}
			if a, err := netip.ParseAddr(strings.TrimSpace(last)); err == nil {
				return a.Unmap(), true
			}
		}
	}
	return remote, true
}

// FromCloudflare reports whether the request arrived through a Cloudflare edge.
func (c *ClientIP) FromCloudflare(r *http.Request) bool {
	p, ok := c.peer(r)
	return ok && c.IsCloudflare(p)
}

// TrustGeoHeader reports whether GEO_HEADER may be honoured for this request:
// always when GEO_TRUST_CLOUDFLARE_ONLY=false, else only for Cloudflare-originated requests.
func (c *ClientIP) TrustGeoHeader(r *http.Request) bool {
	return !c.cfOnly || c.FromCloudflare(r)
}

// IP returns the trusted client IP (never empty unless RemoteAddr is malformed).
func (c *ClientIP) IP(r *http.Request) string {
	if c.cfOnly {
		if cf := r.Header.Get("CF-Connecting-IP"); cf != "" && c.FromCloudflare(r) {
			if a, err := netip.ParseAddr(strings.TrimSpace(cf)); err == nil {
				return a.Unmap().String()
			}
		}
	}
	if c.trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first := xff
			if i := strings.IndexByte(xff, ','); i >= 0 {
				first = xff[:i]
			}
			first = strings.TrimSpace(first)
			if a, err := netip.ParseAddr(first); err == nil {
				return a.Unmap().String()
			}
		}
	}
	if a, ok := parseHostPort(r.RemoteAddr); ok {
		return a.String()
	}
	return r.RemoteAddr
}

func parseHostPort(s string) (netip.Addr, bool) {
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap(), true
	}
	if a, err := netip.ParseAddr(s); err == nil {
		return a.Unmap(), true
	}
	return netip.Addr{}, false
}
