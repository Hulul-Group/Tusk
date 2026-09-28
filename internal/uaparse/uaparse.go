// Package uaparse reproduces Kutt's visit enrichment buckets exactly:
//
//   - Browser/OS: express-useragent 2.2.1 (UserAgent.hydrate: getOS, getPlatform,
//     getBrowser, testMobile, testCaptiveNetwork) folded through Kutt's
//     server/utils/utils.js getUseragentBrowser (l.360-373) and getUseragentOS (l.379-394).
//   - IsBot: isbot 5.1.19 (links.handler.js:532).
//
// Browser and OS use no regexp and do not allocate. The express-useragent regexes
// used are all case-insensitive, so matching is done with ASCII case folding.
//
// Faithful quirks (kept on purpose, Kutt is the source of truth):
//   - Safari is never reported: getUseragentBrowser has no isSafari branch, so Safari
//     visits land in "other" and visits.br_safari stays 0 (utils.js:360-373).
//   - Edge UAs (contain "Edg") are matched by express-useragent before Chrome, so they
//     are "edge" even though they also contain "Chrome".
package uaparse

import (
	"strings"
	"unsafe"
)

// flags mirrors the subset of express-useragent AgentDetails that Kutt reads.
type flags struct {
	isIE, isFirefox, isChrome, isOpera, isEdge bool

	isWindows, isMac, isLinux, isChromeOS          bool
	isiPhone, isiPad, isiPod, isAndroid, isSamsung bool
	isBada, isBlackberry, isWindowsPhone           bool
	isDesktop, isMobile                            bool
}

// Browser returns one of chrome|edge|firefox|ie|opera|other (Kutt never emits safari).
func Browser(ua string) string {
	var buf [lowerBuf]byte
	orig := trim(ua)
	var f flags
	f.browser(orig, lowerInto(&buf, orig))
	return browserBucket(&f)
}

// OS returns one of android|ios|linux|macos|windows|other.
func OS(ua string) string {
	var buf [lowerBuf]byte
	orig := trim(ua)
	var f flags
	f.parseAll(orig, lowerInto(&buf, orig))
	return osBucket(&f)
}

// Parse returns both buckets with a single pass over the flags.
func Parse(ua string) (browser, os string) {
	var buf [lowerBuf]byte
	orig := trim(ua)
	var f flags
	f.parseAll(orig, lowerInto(&buf, orig))
	return browserBucket(&f), osBucket(&f)
}

// parseAll: orig is the trimmed UA, s its ASCII-lower-cased copy.
func (f *flags) parseAll(orig, s string) {
	f.getOS(s)
	f.getPlatform(s)
	f.browser(orig, s)
	f.testMobile(s)
	// testCaptiveNetwork runs after testMobile in hydrate(), so it cannot make isDesktop true.
	if containsFold(s, "captivenetwork") {
		f.isMac = true
	}
}

// utils.js getUseragentBrowser
func browserBucket(f *flags) string {
	switch {
	case f.isIE:
		return "ie"
	case f.isFirefox:
		return "firefox"
	case f.isChrome:
		return "chrome"
	case f.isOpera:
		return "opera"
	case f.isEdge:
		return "edge"
	}
	return "other"
}

// utils.js getUseragentOS
func osBucket(f *flags) string {
	switch {
	case f.isWindows:
		return "windows"
	case f.isMac && f.isDesktop:
		return "macos"
	case f.isiPhone || f.isiPad || f.isiPod:
		return "ios"
	case f.isMac && f.isMobile:
		return "ios"
	case f.isAndroid:
		return "android"
	case f.isLinux:
		return "linux"
	}
	return "other"
}

// getBrowser: first match wins.
func (f *flags) browser(orig, s string) {
	switch {
	case containsFold(s, "yabrowser") || containsFold(s, "yowser"):
	case containsFold(s, "alamofire"):
	case containsFold(s, "edg"): // /edge|edga|edgios|edg/i
		f.isEdge = true
	case containsFold(s, "phantomjs"):
	case containsFold(s, "konqueror"), containsFold(s, "amaya"), containsFold(s, "epiphany"),
		containsFold(s, "seamonkey"), containsFold(s, "flock"), containsFold(s, "omniweb"):
	case containsFold(s, "opera") || containsFold(s, "opr/"):
		f.isOpera = true
	case containsFold(s, "chromium"):
		f.isChrome = true
	case strings.Contains(orig, "FBAN") || strings.Contains(orig, "FBAV"): // /FBA[NV]/ is case-sensitive
	case containsFold(s, "chrome") || containsFold(s, "crios"):
		f.isChrome = true
	case containsFold(s, "msapphost"):
	case containsFold(s, "msie") || containsFold(s, "trident"):
		f.isIE = true
	case containsFold(s, "firefox") || containsFold(s, "fxios"):
		f.isFirefox = true
	case containsFold(s, "android") && containsFold(s, "version/") && containsFold(s, "mobile safari/") &&
		!containsFold(s, "chrome") && !containsFold(s, "silk"):
		f.isAndroid = true // "Android Browser"
	}
}

// getOS: first match wins; only the flags matter to Kutt.
func (f *flags) getOS(s string) {
	switch {
	case containsFold(s, "windows nt 6.0"), containsFold(s, "windows nt 6.1"),
		containsFold(s, "windows nt 6.2"), containsFold(s, "windows nt 6.3"),
		windows11(s), containsFold(s, "windows nt 10.0"),
		containsFold(s, "windows nt 5.2"), containsFold(s, "windows nt 5.1"),
		containsFold(s, "windows nt 5.0"):
		f.isWindows = true
	case containsFold(s, "windows phone 8.1"), containsFold(s, "windows phone 8.0"):
		f.isWindowsPhone = true
	case containsFold(s, "linux"):
		f.isLinux = true
	case containsFold(s, "cros"):
		f.isChromeOS = true
	case containsFold(s, "wii"), containsFold(s, "playstation 3"), containsFold(s, "playstation portable"):
	case containsFold(s, "os x"), macOSNoX(s):
		f.isMac = true
	case iosDeviceOS(s, "ipad"):
		f.isiPad = true
	case iosDeviceOS(s, "iphone"):
		f.isiPhone = true
	case bada(s):
		f.isBada = true
	case curlVersion(s):
	case containsFold(s, "ios"):
		f.isiPhone = true
	}
}

// getPlatform: first match wins.
func (f *flags) getPlatform(s string) {
	switch {
	case containsFold(s, "windows nt"):
	case containsFold(s, "windows phone"):
		f.isWindowsPhone = true
	case containsFold(s, "macintosh"):
	case containsFold(s, "curl"):
	case containsFold(s, "electron"):
	case containsFold(s, "android"):
		f.isAndroid = true
		if containsFold(s, "samsung") {
			f.isSamsung = true
		}
	case containsFold(s, "blackberry"):
		f.isBlackberry = true
	case containsFold(s, "linux"), containsFold(s, "wii"), containsFold(s, "playstation"):
	case containsFold(s, "ipad"):
		f.isiPad = true
	case containsFold(s, "ipod"):
		f.isiPod = true
	case containsFold(s, "iphone"):
		f.isiPhone = true
	case containsFold(s, "samsung"):
		f.isSamsung = true
	}
}

func (f *flags) testMobile(s string) {
	if f.isWindows || f.isLinux || f.isMac || f.isChromeOS {
		f.isDesktop = true
	} else if f.isAndroid || f.isSamsung {
		f.isMobile = true
	}
	if f.isiPad || f.isiPod || f.isiPhone || f.isBada || f.isBlackberry || f.isAndroid || f.isWindowsPhone {
		f.isMobile, f.isDesktop = true, false
	}
	if containsFold(s, "mobile") || hasPrefixFold(s, "ios-") {
		f.isMobile, f.isDesktop = true, false
	}
	if containsFold(s, "okhttp") {
		f.isMobile, f.isAndroid, f.isDesktop = true, true, false
	}
}

// ---------- small matchers (no regexp, no allocation) ----------

// /\bwindows(?:\s|_|)11(?:\.\d+)?/i
func windows11(s string) bool {
	for i := indexFold(s, 0, "windows"); i >= 0; i = indexFold(s, i+1, "windows") {
		if i > 0 && isWord(s[i-1]) {
			continue
		}
		j := i + 7
		if j < len(s) && (isSpace(s[j]) || s[j] == '_') && hasPrefixAt(s, j+1, "11") {
			return true
		}
		if hasPrefixAt(s, j, "11") {
			return true
		}
	}
	return false
}

// mac os (?:x )?(11|12|13|14|15|26)[._]\d+ — the macOS patterns that don't contain "os x".
func macOSNoX(s string) bool {
	for i := indexFold(s, 0, "mac os "); i >= 0; i = indexFold(s, i+1, "mac os ") {
		j := i + 7
		if j+3 < len(s)+0 && j+3 <= len(s) {
			if len(s) >= j+4 && isDigit(s[j]) && isDigit(s[j+1]) && (s[j+2] == '.' || s[j+2] == '_') && isDigit(s[j+3]) {
				switch s[j : j+2] {
				case "11", "12", "13", "14", "15", "26":
					return true
				}
			}
		}
	}
	return false
}

// readIOSDeviceOSMatch: a "(" segment starting with device containing "os " + \d+[._]\d+.
func iosDeviceOS(s, device string) bool {
	from := 0
	for from < len(s) {
		open := indexByteFrom(s, from, '(')
		if open < 0 {
			return false
		}
		cl := indexByteFrom(s, open+1, ')')
		end := len(s)
		if cl >= 0 {
			end = cl
		}
		seg := s[open+1 : end]
		if hasPrefixFold(seg, device) {
			for k := indexFold(seg, 0, "os "); k >= 0; k = indexFold(seg, k+3, "os ") {
				v := k + 3
				for v < len(seg) && seg[v] == ' ' {
					v++
				}
				m := v
				for m < len(seg) && isDigit(seg[m]) {
					m++
				}
				if m > v && m < len(seg) && (seg[m] == '.' || seg[m] == '_') && m+1 < len(seg) && isDigit(seg[m+1]) {
					return true
				}
			}
		}
		from = end + 1
	}
	return false
}

// /Bada\/(\d+)\.(\d+)/i
func bada(s string) bool {
	for i := indexFold(s, 0, "bada/"); i >= 0; i = indexFold(s, i+1, "bada/") {
		j := i + 5
		n := digits(s, j)
		if n > 0 && j+n < len(s) && s[j+n] == '.' && digits(s, j+n+1) > 0 {
			return true
		}
	}
	return false
}

// /curl\/(\d+)\.(\d+)\.(\d+)/i
func curlVersion(s string) bool {
	for i := indexFold(s, 0, "curl/"); i >= 0; i = indexFold(s, i+1, "curl/") {
		j := i + 5
		ok := true
		for part := 0; part < 3; part++ {
			n := digits(s, j)
			if n == 0 {
				ok = false
				break
			}
			j += n
			if part < 2 {
				if j >= len(s) || s[j] != '.' {
					ok = false
					break
				}
				j++
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func digits(s string, i int) int {
	n := 0
	for i+n < len(s) && isDigit(s[i+n]) {
		n++
	}
	return n
}

func trim(s string) string {
	// JS String.prototype.trim; ASCII whitespace is what matters for HTTP headers.
	i, j := 0, len(s)
	for i < j && isSpace(s[i]) {
		i++
	}
	for j > i && isSpace(s[j-1]) {
		j--
	}
	return s[i:j]
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}
func isWord(c byte) bool {
	return c == '_' || isDigit(c) || (c|0x20 >= 'a' && c|0x20 <= 'z')
}

func lower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 32
	}
	return c
}

// indexFold finds sub in the ALREADY LOWER-CASED s at or after from.
func indexFold(s string, from int, sub string) int {
	if from > len(s) {
		return -1
	}
	i := strings.Index(s[from:], sub)
	if i < 0 {
		return -1
	}
	return i + from
}

func containsFold(s, sub string) bool { return strings.Contains(s, sub) }

func hasPrefixFold(s, p string) bool { return len(s) >= len(p) && indexFoldAt(s, 0, p) }

func hasPrefixAt(s string, i int, p string) bool {
	return i >= 0 && i+len(p) <= len(s) && s[i:i+len(p)] == p
}

// indexFoldAt: s (already lower-cased) has p at i.
func indexFoldAt(s string, i int, p string) bool {
	return i >= 0 && i+len(p) <= len(s) && s[i:i+len(p)] == p
}

func indexByteFrom(s string, from int, c byte) int {
	if from >= len(s) {
		return -1
	}
	i := strings.IndexByte(s[from:], c)
	if i < 0 {
		return -1
	}
	return i + from
}

// lowerBuf bounds the stack buffer used for lower-casing; longer UAs fall back to
// one heap allocation (rare: browsers send < 512 bytes).
const lowerBuf = 512

// lowerInto ASCII-lower-cases s into buf (JS /i without the u flag folds ASCII only
// for these patterns). The returned string aliases buf and must not escape.
func lowerInto(buf *[lowerBuf]byte, s string) string {
	var b []byte
	if len(s) <= lowerBuf {
		b = buf[:len(s)]
	} else {
		b = make([]byte, len(s))
	}
	for i := 0; i < len(s); i++ {
		b[i] = lower(s[i])
	}
	return unsafe.String(unsafe.SliceData(b), len(b))
}
