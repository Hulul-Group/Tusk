package uaparse

import (
	"regexp"
	"strings"
)

// IsBot ports isbot 5.1.19 (the version pinned by Kutt, package.json) used at
// links.handler.js:532. isbot(ua) is `Boolean(ua) && /pattern/i.test(ua)`, so an
// EMPTY User-Agent is NOT a bot (verified in node_modules/isbot/index.js).
//
// Go's regexp has no DFA: the 190-way upstream alternation costs ~200µs per UA on
// the NFA. The pattern list is therefore split by shape (every entry from
// src/patterns.json is accounted for below and verified against Node on ~3.2k UAs):
//
//   - substrLits: plain case-insensitive substrings, matched with a 2-byte bucket index;
//   - prefixLits: `^literal` entries;
//   - hand-written matchers for entries with lookaround / \b / tiny classes;
//   - anchoredMatch: `^...` regex entries, hand-coded or behind a cheap prefix gate.
func IsBot(ua string) bool {
	if ua == "" {
		return false
	}
	var buf [lowerBuf]byte
	s := lowerInto(&buf, ua)
	return matchSubstr(s) || matchPrefix(s) || lookaround(s) || special(s) || anchoredMatch(s, ua)
}

// Unanchored literal entries (regex escapes removed, char classes expanded).
var substrLits = []string{
	" daum/", " daum ", " deusu/", " yadirectfetcher", "()",
	"adscanner/", "analyzer", "archive", "ask jeeves/teoma", "bit.ly/", "bluecoat drtr",
	"browsex", "burpcollaborator", "capture", "catch", "checker", "chrome-lighthouse",
	"chromeframe", "classifier", "cloudflare", "convertify", "cookiehubscan", "crawl", "cypress/",
	"dareboost", "datanyze", "dejaclick", "detect", "dmbrowser", "download", "evc-batch/",
	"exaleadcloudview", "feed", "firephp", "functionize", "gomezagent", "headless", "httrack",
	"hubspot marketing grader", "hydra", "ibisbrowser", "images", "infrawatch", "insight",
	"inspect", "iplabel", "ips-agent", "jsjcw_scanner", "library", "linkcheck", "mail.ru/",
	"manager", "measure", "neustar wpm", "node", "nutch", "offbyone", "optimize", "pageburst",
	"pagespeed", "parser", "perl", "phantomjs", "pingdom", "powermarks", "preview", "proxy",
	"reputation", "resolver", "retriever", "rexx;", "rigor", "scanner.",
	"scrape", "server", "sogou", "sparkler/", "speedcurve", "spider", "splash", "statuscake",
	"supercleaner", "synapse", "synthetic", "tools", "torrent", "trace", "transcoder", "url",
	"validator", "virtuoso", "wappalyzer", "webglance", "webkit2png", "whatcms/", "zgrab",
}

// `^literal` entries.
var prefixLits = []string{
	"<", "active", "ad muncher", "amaya", "avsdevicesdk/", "biglotron", "bot", "bw/",
	"clamav ", "clamav/", "client/", "cobweb/", "custom", "ddg_android", "ddg-android", "discourse",
	"downcast/", "duckduckgo", "facebook", "getright/", "gozilla/", "hobbit", "hotzonu", "hwcdn/",
	"igetter/", "jeode/", "jetty/", "jigsaw", "microsoft bits", "movabletype", "navermailapp",
	"netsurf", "offline", "openai/", "owler", "php", "postman", "python", "rank", "read", "reed",
	"rest", "rss", "snapchat", "space bison", "svn", "swcd ", "taringa", "thumbor/", "track",
	"w3c", "webbandit/", "webcopier", "wget", "whatsapp", "wordpress", "xenu link sleuth",
	"yahoo", "yandex", "zoom marketplace/",
}

// Anchored regexes that are only reachable behind a cheap first-bytes gate, verbatim
// from isbot (with (?i)). They receive the ORIGINAL string (the lower-case stack buffer
// must not escape).
var (
	reMoz1   = regexp.MustCompile(`(?i)^mozilla/5\.0\s[a-z\.-]+$`)
	reMoz2   = regexp.MustCompile(`(?i)^mozilla/\d\.\d \(compatible;?\)$`)
	reMoz3   = regexp.MustCompile(`(?i)^mozilla/\d\.\d \w*$`)
	reBraces = regexp.MustCompile(`^\{\{.*\}\}$`)
)

// anchoredMatch covers the ^-anchored regex entries. s is lower-cased, orig is the raw UA.
//
//	^[\w \.\-\(?:\):]+(?:/v?\d+(?:\.\d+)?(?:\.\d{1,10})*?)?(?:,|$)   -> productOnly
//	^[^ ]{50,}$                                                    -> inline
//	^\d+\b   ^\w*search\b   ^\w+/[\w\(\)]*$                          -> inline
//	^mozilla/…(3)   ^{{.*}}$                                       -> gated regexes
func anchoredMatch(s, orig string) bool {
	if productOnly(s) {
		return true
	}
	if len(s) >= 50 && indexByteFrom(s, 0, ' ') < 0 {
		return true
	}
	w := 0
	for w < len(s) && isWord(s[w]) {
		w++
	}
	// ^\d+\b: maximal digit run must end at a non-word char or end.
	d := digits(s, 0)
	if d > 0 && (d == len(s) || !isWord(s[d])) {
		return true
	}
	// ^\w*search\b: "search" must end the maximal word run.
	if w >= 6 && s[w-6:w] == "search" {
		return true
	}
	// ^\w+/[\w\(\)]*$
	if w > 0 && w < len(s) && s[w] == '/' {
		ok := true
		for i := w + 1; i < len(s); i++ {
			if c := s[i]; !isWord(c) && c != '(' && c != ')' {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	if strings.HasPrefix(s, "mozilla/") &&
		(reMoz1.MatchString(orig) || reMoz2.MatchString(orig) || reMoz3.MatchString(orig)) {
		return true
	}
	return s[0] == '{' && reBraces.MatchString(orig)
}

func isProductClass(c byte) bool {
	return isWord(c) || c == ' ' || c == '.' || c == '-' || c == '(' || c == '?' || c == ':' || c == ')'
}

// productOnly implements ^[\w .\-(?:):]+(?:/v?\d+(?:\.\d+)?(?:\.\d{1,10})*?)?(?:,|$).
// The class excludes '/' and ',', so the leading run is always maximal; what follows must be
// end, ',' or a "/v?N(.N)(.N{1,10})*" version followed by end or ','.
func productOnly(s string) bool {
	k := 0
	for k < len(s) && isProductClass(s[k]) {
		k++
	}
	if k == 0 {
		return false
	}
	if k == len(s) || s[k] == ',' {
		return true
	}
	if s[k] != '/' {
		return false
	}
	i := k + 1
	if i < len(s) && s[i] == 'v' {
		i++
	}
	for seg := 0; ; seg++ {
		n := digits(s, i)
		if n == 0 || (seg >= 2 && n > 10) {
			return false
		}
		i += n
		if i == len(s) || s[i] == ',' {
			return true
		}
		if s[i] != '.' {
			return false
		}
		i++
	}
}

// 2-byte bucket index over substrLits: litPair[first<<8|second] = bucket number (0 = none).
var (
	litBuckets [][]string
	litPair    [1 << 16]uint8
)

func init() {
	for _, l := range substrLits {
		k := uint16(l[0])<<8 | uint16(l[1])
		if litPair[k] == 0 {
			litBuckets = append(litBuckets, nil)
			litPair[k] = uint8(len(litBuckets))
		}
		b := litPair[k] - 1
		litBuckets[b] = append(litBuckets[b], l)
	}
}

// matchSubstr: s is lower-cased.
func matchSubstr(s string) bool {
	for i := 0; i+1 < len(s); i++ {
		b := litPair[uint16(s[i])<<8|uint16(s[i+1])]
		if b == 0 {
			continue
		}
		for _, l := range litBuckets[b-1] {
			if indexFoldAt(s, i, l) {
				return true
			}
		}
	}
	return false
}

// matchPrefix: s is lower-cased.
func matchPrefix(s string) bool {
	c := s[0]
	for _, p := range prefixLits {
		if p[0] == c && indexFoldAt(s, 0, p) {
			return true
		}
	}
	return false
}

// special covers entries with \b, small classes or a trailing \d:
//
//	@[a-z][\w-]+\.   \.com\b   \btime/   check\b   rss\b
//	ptst[ /]\d       ^dispatch/\d   ^zdm/\d
func special(s string) bool {
	for i := indexByteFrom(s, 0, '@'); i >= 0; i = indexByteFrom(s, i+1, '@') {
		j := i + 1
		if j < len(s) && lower(s[j]) >= 'a' && lower(s[j]) <= 'z' {
			k := j + 1
			for k < len(s) && (isWord(s[k]) || s[k] == '-') {
				k++
			}
			// [\w-]+ needs >=1 char and must be followed by '.', with backtracking
			// any '.' after >=1 class chars works; the greedy run stops at the first non-class byte.
			if k > j+1 && k < len(s) && s[k] == '.' {
				return true
			}
		}
	}
	if wordEnd(s, ".com") || wordEnd(s, "check") || wordEnd(s, "rss") {
		return true
	}
	for i := indexFold(s, 0, "time/"); i >= 0; i = indexFold(s, i+1, "time/") {
		if i == 0 || !isWord(s[i-1]) {
			return true
		}
	}
	for i := indexFold(s, 0, "ptst"); i >= 0; i = indexFold(s, i+1, "ptst") {
		if i+5 < len(s) && (s[i+4] == ' ' || s[i+4] == '/') && isDigit(s[i+5]) {
			return true
		}
	}
	if (indexFoldAt(s, 0, "dispatch/") && len(s) > 9 && isDigit(s[9])) ||
		(indexFoldAt(s, 0, "zdm/") && len(s) > 4 && isDigit(s[4])) {
		return true
	}
	return false
}

// wordEnd: lit followed by a word boundary (lit ends with a word char).
func wordEnd(s, lit string) bool {
	for i := indexFold(s, 0, lit); i >= 0; i = indexFold(s, i+1, lit) {
		j := i + len(lit)
		if j >= len(s) || !isWord(s[j]) {
			return true
		}
	}
	return false
}

// lookaround evaluates the six isbot alternatives that need lookaround (RE2 can't):
//
//	(?:^|[^g])news(?!sapphire)
//	(?<! (?:channel/|google/))google(?!(app|/google| pixel))
//	(?<! cu)bots?(?:\b|_)
//	(?<!(?:lib))http
//	(?<![hg]m)score
//	java(?!;)
func lookaround(s string) bool {
	for i := indexFold(s, 0, "news"); i >= 0; i = indexFold(s, i+1, "news") {
		if (i == 0 || lower(s[i-1]) != 'g') && !indexFoldAt(s, i+4, "sapphire") {
			return true
		}
	}
	for i := indexFold(s, 0, "google"); i >= 0; i = indexFold(s, i+1, "google") {
		if endsFoldAt(s, i, " channel/") || endsFoldAt(s, i, " google/") {
			continue
		}
		j := i + 6
		if indexFoldAt(s, j, "app") || indexFoldAt(s, j, "/google") || indexFoldAt(s, j, " pixel") {
			continue
		}
		return true
	}
	for i := indexFold(s, 0, "bot"); i >= 0; i = indexFold(s, i+1, "bot") {
		if endsFoldAt(s, i, " cu") {
			continue
		}
		j := i + 3 // bots?(?:\b|_): try "bots" then "bot"
		if j < len(s) && lower(s[j]) == 's' && boundaryOrUnderscore(s, j+1) {
			return true
		}
		if boundaryOrUnderscore(s, j) {
			return true
		}
	}
	for i := indexFold(s, 0, "http"); i >= 0; i = indexFold(s, i+1, "http") {
		if !endsFoldAt(s, i, "lib") {
			return true
		}
	}
	for i := indexFold(s, 0, "score"); i >= 0; i = indexFold(s, i+1, "score") {
		if i >= 2 && lower(s[i-1]) == 'm' && (lower(s[i-2]) == 'h' || lower(s[i-2]) == 'g') {
			continue
		}
		return true
	}
	for i := indexFold(s, 0, "java"); i >= 0; i = indexFold(s, i+1, "java") {
		if i+4 >= len(s) || s[i+4] != ';' {
			return true
		}
	}
	return false
}

// endsFoldAt reports whether s[:i] ends with p (case-insensitive, p lower-case).
func endsFoldAt(s string, i int, p string) bool {
	return i >= len(p) && indexFoldAt(s, i-len(p), p)
}

// boundaryOrUnderscore: JS `(?:\b|_)` at position j where s[j-1] is a word char.
func boundaryOrUnderscore(s string, j int) bool {
	if j >= len(s) {
		return true
	}
	return !isWord(s[j]) || s[j] == '_'
}
