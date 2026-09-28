package api

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/dlclark/regexp2"
)

// This file ports the primitives behind Kutt's validators.handler.js:
// express-validator 6.14 value coercion, validator 13 isEmail/isNumeric/
// isBoolean/isLength, utils.urlRegex (needs lookahead -> regexp2), the `ms`
// 2.1.3 parser, Node's legacy url.parse host extraction and parseInt.

// ---------- JS value helpers ----------

// jsString is express-validator's utils.toString: arrays -> first element,
// null/undefined -> "", numbers/bools -> String(v), objects -> "[object Object]".
func jsString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		if math.IsNaN(x) {
			return ""
		}
		return jsNumber(x)
	case []any:
		if len(x) == 0 {
			return ""
		}
		return jsStringShallow(x[0])
	case map[string]any:
		return "[object Object]"
	}
	return ""
}

func jsStringShallow(v any) string {
	if a, ok := v.([]any); ok {
		// String([a,b]) = "a,b"
		parts := make([]string, len(a))
		for i, e := range a {
			parts[i] = jsStringShallow(e)
		}
		return strings.Join(parts, ",")
	}
	return jsString(v)
}

func jsNumber(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e21 {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// truthy is JavaScript truthiness.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0 && !math.IsNaN(x)
	}
	return true
}

func isString(v any) bool { _, ok := v.(string); return ok }

// jsTrim is String.prototype.trim (validator's trim() with no chars uses \s).
func jsTrim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\v', '\f', '\r', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
			return true
		}
		return r >= 0x2000 && r <= 0x200a
	})
}

// jsLen is validator.isLength's length: code points (surrogate pairs count once).
func jsLen(s string) int { return utf8.RuneCountInString(s) }

// sanitizeCheckbox: value === true || value === "on" || value.
func sanitizeCheckbox(v any) any {
	if b, ok := v.(bool); ok && b {
		return true
	}
	if s, ok := v.(string); ok && s == "on" {
		return true
	}
	return v
}

// isBooleanStrict is validator.isBoolean (loose=false): "true","false","1","0".
func isBooleanStrict(v any) bool {
	switch jsString(v) {
	case "true", "false", "1", "0":
		return true
	}
	return false
}

// boolish interprets a checkbox/boolean after validation.
func boolish(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "true" || x == "1" || x == "on"
	case float64:
		return x == 1
	}
	return false
}

var numericRe = regexp.MustCompile(`^[+-]?([0-9]*[.])?[0-9]+$`)

// isNumeric is validator.isNumeric (no locale).
func isNumeric(s string) bool { return numericRe.MatchString(s) }

// parseIntJS ports parseInt(s, 10): optional whitespace and sign, then the
// longest digit prefix. ok=false for NaN.
func parseIntJS(s string) (int64, bool) {
	s = jsTrim(s)
	neg := false
	if s != "" && (s[0] == '+' || s[0] == '-') {
		neg = s[0] == '-'
		s = s[1:]
	}
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, false
	}
	n, err := strconv.ParseInt(s[:i], 10, 64)
	if err != nil {
		n = math.MaxInt64
	}
	if neg {
		n = -n
	}
	return n, true
}

// ---------- ms 2.1.3 ----------

var msRe = regexp.MustCompile(`(?i)^(-?(?:\d+)?\.?\d+) *(milliseconds?|msecs?|ms|seconds?|secs?|s|minutes?|mins?|m|hours?|hrs?|h|days?|d|weeks?|w|years?|yrs?|y)?$`)

const (
	msS = 1000.0
	msM = msS * 60
	msH = msM * 60
	msD = msH * 24
	msW = msD * 7
	msY = msD * 365.25
)

// parseMS ports ms(str) for strings. ok=false where ms returns undefined.
func parseMS(str string) (float64, bool) {
	if len(utf16.Encode([]rune(str))) > 100 {
		return 0, false
	}
	m := msRe.FindStringSubmatch(str)
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	switch strings.ToLower(m[2]) {
	case "years", "year", "yrs", "yr", "y":
		return n * msY, true
	case "weeks", "week", "w":
		return n * msW, true
	case "days", "day", "d":
		return n * msD, true
	case "hours", "hour", "hrs", "hr", "h":
		return n * msH, true
	case "minutes", "minute", "mins", "min", "m":
		return n * msM, true
	case "seconds", "second", "secs", "sec", "s":
		return n * msS, true
	case "milliseconds", "millisecond", "msecs", "msec", "ms", "":
		return n, true
	}
	return 0, false
}

func jsRound(x float64) float64 { return math.Floor(x + 0.5) }

// fmtMSLong ports ms(n, {long: true}).
func fmtMSLong(ms float64) string {
	abs := math.Abs(ms)
	plural := func(n float64, name string) string {
		s := jsNumber(jsRound(ms/n)) + " " + name
		if abs >= n*1.5 {
			s += "s"
		}
		return s
	}
	switch {
	case abs >= msD:
		return plural(msD, "day")
	case abs >= msH:
		return plural(msH, "hour")
	case abs >= msM:
		return plural(msM, "minute")
	case abs >= msS:
		return plural(msS, "second")
	}
	return jsNumber(ms) + " ms"
}

// ---------- regexes from utils.js ----------

// urlRegex is utils.js:26 verbatim. JS evaluates it over UTF-16 code units
// (no /u flag), so input is matched as UTF-16 units via matchJS.
var urlRegex = regexp2.MustCompile(`^(?:(?:(?:https?|ftp):)?\/\/)(?:\S+(?::\S*)?@)?(?:(?!(?:10|127)(?:\.\d{1,3}){3})(?!(?:169\.254|192\.168)(?:\.\d{1,3}){2})(?!172\.(?:1[6-9]|2\d|3[0-1])(?:\.\d{1,3}){2})(?:[1-9]\d?|1\d\d|2[01]\d|22[0-3])(?:\.(?:1?\d{1,2}|2[0-4]\d|25[0-5])){2}(?:\.(?:[1-9]\d?|1\d\d|2[0-4]\d|25[0-4]))|(?:(?:[a-z0-9\u00a1-\uffff][a-z0-9\u00a1-\uffff_-]{0,62})?[a-z0-9\u00a1-\uffff]\.)+(?:[a-z\u00a1-\uffff]{2,}\.?))(?::\d{2,5})?(?:[/?#]\S*)?$`, regexp2.IgnoreCase|regexp2.ECMAScript)

// otherSchemeRegex: /^(?!https?|ftp)(\w+:|\/\/)/ (validators.handler.js:29).
var otherSchemeRegex = regexp2.MustCompile(`^(?!https?|ftp)(\w+:|\/\/)`, regexp2.ECMAScript)

var hasProtocolRe = regexp.MustCompile(`^(\w+:|//)`)

func matchJS(re *regexp2.Regexp, s string) bool {
	u := utf16.Encode([]rune(s))
	rs := make([]rune, len(u))
	for i, c := range u {
		rs[i] = rune(c)
	}
	ok, err := re.MatchRunes(rs)
	return err == nil && ok
}

// addProtocol ports utils.addProtocol.
func addProtocol(u string) string {
	if hasProtocolRe.MatchString(u) {
		return u
	}
	return "http://" + u
}

// validTarget: urlRegex || /^(?!https?|ftp)(\w+:|\/\/)/
func validTarget(v string) bool {
	return matchJS(urlRegex, v) || matchJS(otherSchemeRegex, v)
}

var customAddressRe = regexp.MustCompile(`^[a-zA-Z0-9\-_]+$`)

// newCustomAlphabetRe builds utils.customAlphabetRegex for LINK_CUSTOM_ALPHABET.
func newCustomAlphabetRe(alphabet string) *regexp.Regexp {
	var b strings.Builder
	for _, c := range alphabet {
		if strings.ContainsRune(`.$*+?()[]{}|^-\`, c) {
			b.WriteByte('\\')
		}
		b.WriteRune(c)
	}
	re, err := regexp.Compile(`^[` + b.String() + `_\-]+$`)
	if err != nil {
		return customAddressRe
	}
	return re
}

// ---------- validator.isEmail (13.x, default options) ----------

var (
	emailUserPart   = regexp.MustCompile("(?i)^[a-z\\d!#\\$%&'\\*\\+\\-/=\\?\\^_`{\\|}~\u00A1-\uD7FF\uF900-\uFDCF\uFDF0-\uFFEF]+$")
	quotedEmailUser = regexp.MustCompile("(?i)^([\\s\\x01-\\x08\\x0b\\x0c\\x0e-\\x1f\\x7f\\x21\\x23-\\x5b\\x5d-\\x7e\u00A1-\uD7FF\uF900-\uFDCF\uFDF0-\uFFEF]|(\\\\[\\x01-\\x09\\x0b\\x0c\\x0d-\\x7f\u00A1-\uD7FF\uF900-\uFDCF\uFDF0-\uFFEF]))*$")
	fqdnTLD         = regexp.MustCompile("(?i)^([a-z\u00A1-\u00A8\u00AA-\uD7FF\uF900-\uFDCF\uFDF0-\uFFEF]{2,}|xn[a-z0-9-]{2,})$")
	fqdnPart        = regexp.MustCompile("(?i)^[a-z_\u00a1-\uffff0-9-]+$")
	fullWidth       = regexp.MustCompile("[\uff01-\uff5e]")
)

func isEmail(str string) bool {
	if len(utf16.Encode([]rune(str))) > 254 {
		return false
	}
	at := strings.LastIndex(str, "@")
	if at < 0 {
		return false
	}
	domainPart := strings.ToLower(str[at+1:])
	user := str[:at]
	if len(user) > 64 || len(domainPart) > 254 {
		return false
	}
	if !isFQDN(domainPart) {
		return false
	}
	if strings.HasPrefix(user, `"`) {
		if len(user) < 2 {
			return false
		}
		return quotedEmailUser.MatchString(user[1 : len(user)-1])
	}
	if user == "" {
		return false
	}
	for _, p := range strings.Split(user, ".") {
		if !emailUserPart.MatchString(p) {
			return false
		}
	}
	return true
}

func isFQDN(str string) bool {
	// validator 13 isFQDN, allow_trailing_dot=false: a trailing dot leaves an empty TLD.
	parts := strings.Split(str, ".")
	tld := parts[len(parts)-1]
	if len(parts) < 2 {
		return false
	}
	if !fqdnTLD.MatchString(tld) {
		return false
	}
	if strings.ContainsAny(tld, " \t\n\r\f\v") {
		return false
	}
	if isAllDigits(tld) {
		return false
	}
	for _, part := range parts {
		if len(part) > 63 {
			return false
		}
		if !fqdnPart.MatchString(part) || fullWidth.MatchString(part) {
			return false
		}
		if strings.HasPrefix(part, "-") || strings.HasSuffix(part, "-") {
			return false
		}
		if strings.Contains(part, "_") {
			return false
		}
	}
	return true
}

func isAllDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

// ---------- Node legacy url.parse (host part) ----------

var slashedProtocols = map[string]bool{"http:": true, "https:": true, "ftp:": true, "gopher:": true, "file:": true, "ws:": true, "wss:": true}
var hostlessProtocols = map[string]bool{"javascript:": true}
var protoRe = regexp.MustCompile(`(?i)^[a-z0-9.+-]+:`)
var userAtHostRe = regexp.MustCompile(`^//[^@/]+@[^@/]+`)

// legacyURL holds the parts of url.parse(...) Kutt reads.
type legacyURL struct {
	Host     string // hostname[:port], lower-cased
	Hostname string
}

// parseLegacyURL approximates Node's url.parse for host extraction.
func parseLegacyURL(s string) legacyURL {
	rest := jsTrim(s)
	proto := ""
	if m := protoRe.FindString(rest); m != "" {
		proto = strings.ToLower(m)
		rest = rest[len(m):]
	}
	slashes := false
	// url.js: `if (slashesDenoteHost || proto || /^\/\/[^@/]+@[^@/]+/.test(rest))`
	if (proto != "" || userAtHostRe.MatchString(rest)) && strings.HasPrefix(rest, "//") && !(proto != "" && hostlessProtocols[proto]) {
		slashes = true
		rest = rest[2:]
	}
	if hostlessProtocols[proto] || !(slashes || (proto != "" && !slashedProtocols[proto])) {
		return legacyURL{}
	}
	hostEnd := strings.IndexAny(rest, "/?#")
	search := rest
	if hostEnd >= 0 {
		search = rest[:hostEnd]
	}
	if at := strings.LastIndex(search, "@"); at >= 0 {
		rest = rest[at+1:]
	}
	if i := strings.IndexAny(rest, "<>\"` \r\n\t{}|\\^'%/?;#"); i >= 0 {
		rest = rest[:i]
	}
	host := rest
	hostname := host
	port := ""
	if i := strings.LastIndexByte(host, ':'); i >= 0 && isAllDigitsOrEmpty(host[i+1:]) && !strings.HasSuffix(host, "]") {
		hostname, port = host[:i], host[i+1:]
	}
	hostname = strings.ToLower(hostname)
	if strings.HasPrefix(hostname, "[") && strings.HasSuffix(hostname, "]") {
		hostname = hostname[1 : len(hostname)-1]
	}
	h := hostname
	if strings.Contains(h, ":") {
		h = "[" + h + "]"
	}
	if port != "" {
		h += ":" + port
	}
	return legacyURL{Host: h, Hostname: hostname}
}

func isAllDigitsOrEmpty(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// removeWww ports utils.removeWww (first "www." anywhere).
func removeWww(host string) string {
	i := strings.Index(host, "www.")
	if i < 0 {
		return host
	}
	return host[:i] + host[i+4:]
}
