package redirect

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/Hulul-Group/Tusk/internal/domain"
	"github.com/Hulul-Group/Tusk/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// Kutt messages (validators.handler.js:154-163, links.handler.js:553-561).
const (
	msgPasswordInvalid = "Password is invalid."
	msgPasswordLength  = "Password length must be between 3 and 64."
	msgIDInvalid       = "ID is invalid."
	msgNoLink          = "Couldn't find the link."
	msgWrongPassword   = "Password is not correct."
)

// ServeProtected is POST /api{,/v2}/links/{id}/protected (redirectProtected).
// JSON clients get {"target": url} (200) or {"error": msg}. HTML form posts
// (no JS) get a 302 to the target or the protected page with the error.
func (h *Handler) ServeProtected(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	html := IsHTML(r)
	fail := func(status int, msg string) {
		if html {
			h.pages.protected(w, status, id, msg)
			return
		}
		writeJSONError(w, status, msg)
	}
	pw, present, isString := readPassword(r)
	// express-validator runs body("password") then param("id"); first error wins.
	switch {
	case !present:
		fail(http.StatusBadRequest, msgPasswordInvalid)
		return
	case !isString:
		fail(http.StatusBadRequest, msgPasswordInvalid)
		return
	case utf8.RuneCountInString(pw) < 3 || utf8.RuneCountInString(pw) > 64:
		fail(http.StatusBadRequest, msgPasswordLength)
		return
	}
	if utf8.RuneCountInString(id) != 36 {
		fail(http.StatusBadRequest, msgIDInvalid)
		return
	}
	uuid := id
	l, err := h.o.Finder.Find(r.Context(), store.LinkFilter{UUID: &uuid})
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		fail(http.StatusInternalServerError, "An error occurred.")
		return
	}
	if l == nil || err != nil || l.Password == nil || *l.Password == "" {
		fail(http.StatusBadRequest, msgNoLink)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(*l.Password), trunc72(pw)) != nil {
		fail(http.StatusUnauthorized, msgWrongPassword)
		return
	}
	// Kutt records the visit here without an isbot check (links.handler.js:565).
	if l.UserID != nil {
		h.publish(r, l.ID, l.UserID, r.Header.Get("User-Agent"))
	}
	h.count("protected_ok")
	if html {
		redirect302(w, l.Target)
		return
	}
	writeJSONTarget(w, http.StatusOK, l.Target)
}

// readPassword reads "password" from a JSON or form body.
// present=false when missing/empty/null (exists({checkFalsy,checkNull})).
func readPassword(r *http.Request) (pw string, present, isString bool) {
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		return "", false, false
	}
	switch ct {
	case "application/json":
		var m map[string]json.RawMessage
		if json.Unmarshal(body, &m) != nil {
			return "", false, false
		}
		raw, ok := m["password"]
		if !ok {
			return "", false, false
		}
		var v any
		if json.Unmarshal(raw, &v) != nil {
			return "", false, false
		}
		switch x := v.(type) {
		case nil:
			return "", false, false
		case string:
			return x, x != "", true
		case bool:
			return "", x, false
		case float64:
			return "", x != 0, false
		default:
			return "", true, false
		}
	default:
		vals, err := url.ParseQuery(string(body))
		if err != nil {
			return "", false, false
		}
		v := vals.Get("password")
		return v, v != "", true
	}
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	b, _ := json.Marshal(struct {
		Error string `json:"error"`
	}{msg})
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

func writeJSONTarget(w http.ResponseWriter, status int, target string) {
	b, _ := json.Marshal(struct {
		Target string `json:"target"`
	}{target})
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

// ---------- encodeurl 2.0.0 ----------

// EncodeURL ports the `encodeurl` package used by Express res.location:
// percent-encode every byte outside the URL-safe set while leaving existing
// valid %XX escapes intact. Fast path: returns target unchanged (no alloc).
func EncodeURL(s string) string {
	i := 0
	for ; i < len(s); i++ {
		if !urlSafe(s, i) {
			break
		}
	}
	if i == len(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 16)
	b.WriteString(s[:i])
	const hex = "0123456789ABCDEF"
	for ; i < len(s); i++ {
		if urlSafe(s, i) {
			b.WriteByte(s[i])
			continue
		}
		c := s[i]
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
}

// urlSafe: [\x21\x23-\x3B\x3D\x3F-\x5F\x61-\x7A\x7C\x7E] or a well-formed %XX.
func urlSafe(s string, i int) bool {
	c := s[i]
	switch {
	case c == '%':
		return i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2])
	case c == 0x21, c >= 0x23 && c <= 0x3B, c == 0x3D, c >= 0x3F && c <= 0x5F,
		c >= 0x61 && c <= 0x7A, c == 0x7C, c == 0x7E:
		return true
	}
	return false
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}
