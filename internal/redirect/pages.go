package redirect

import (
	"bytes"
	"embed"
	"html/template"
	"net/http"
	"strings"
)

//go:embed templates/*.html
var templateFS embed.FS

type pages struct {
	t           *template.Template
	siteName    string
	notFound404 []byte
	banned200   []byte
	error500    []byte
}

type pageData struct {
	SiteName string
	Title    string
	ID       string
	Error    string
	Target   string
	Link     string
}

func newPages(siteName string) *pages {
	t := template.Must(template.ParseFS(templateFS, "templates/*.html"))
	p := &pages{t: t, siteName: siteName}
	// Static pages are rendered once.
	p.notFound404 = p.render("notfound.html", pageData{Title: "404 - Not found"})
	p.banned200 = p.render("banned.html", pageData{Title: "Banned link"})
	p.error500 = p.render("error.html", pageData{Title: "Error"})
	return p
}

func (p *pages) render(name string, d pageData) []byte {
	d.SiteName = p.siteName
	var b bytes.Buffer
	if err := p.t.ExecuteTemplate(&b, name, d); err != nil {
		return []byte("<!doctype html><title>Error</title><p>An error occurred.</p>")
	}
	return b.Bytes()
}

func writeHTML(w http.ResponseWriter, status int, body []byte) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (p *pages) notFound(w http.ResponseWriter) { writeHTML(w, http.StatusNotFound, p.notFound404) }
func (p *pages) banned(w http.ResponseWriter)   { writeHTML(w, http.StatusOK, p.banned200) }
func (p *pages) errorPage(w http.ResponseWriter) {
	writeHTML(w, http.StatusInternalServerError, p.error500)
}

func (p *pages) protected(w http.ResponseWriter, status int, uuid, errMsg string) {
	writeHTML(w, status, p.render("protected.html", pageData{Title: "Protected short link", ID: uuid, Error: errMsg}))
}

func (p *pages) info(w http.ResponseWriter, target, link string) {
	writeHTML(w, http.StatusOK, p.render("info.html", pageData{Title: "Short link information", Target: target, Link: link}))
}

// IsHTML ports locals.isHTML: req.accepts(["json","html"]) === "html"
// (Express/negotiator: highest q wins, then specificity, then Accept order,
// then our order, where "json" is listed first).
func IsHTML(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	if accept == "" {
		return false // no Accept header -> first offered type ("json")
	}
	jq, js, jo := matchAccept(accept, "application", "json")
	hq, hs, ho := matchAccept(accept, "text", "html")
	if hq <= 0 {
		return false
	}
	if jq <= 0 {
		return true
	}
	if hq != jq {
		return hq > jq
	}
	if hs != js {
		return hs > js
	}
	return ho < jo
}

// matchAccept returns (q, specificity, order) of the best Accept entry matching type/sub.
func matchAccept(accept, typ, sub string) (q float64, spec int, order int) {
	q, spec, order = -1, -1, 1<<30
	for i, part := range strings.Split(accept, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		mt := part
		pq := 1.0
		if j := strings.IndexByte(part, ';'); j >= 0 {
			mt = strings.TrimSpace(part[:j])
			for _, prm := range strings.Split(part[j+1:], ";") {
				prm = strings.TrimSpace(prm)
				if strings.HasPrefix(prm, "q=") {
					pq = parseQ(prm[2:])
				}
			}
		}
		slash := strings.IndexByte(mt, '/')
		if slash < 0 {
			continue
		}
		t, s := strings.ToLower(mt[:slash]), strings.ToLower(mt[slash+1:])
		sp := 0
		if t == typ {
			sp |= 4
		} else if t != "*" {
			continue
		}
		if s == sub {
			sp |= 2
		} else if s != "*" {
			continue
		}
		if sp > spec || (sp == spec && pq > q) || (sp == spec && pq == q && i < order) {
			q, spec, order = pq, sp, i
		}
	}
	return
}

func parseQ(s string) float64 {
	s = strings.TrimSpace(s)
	var v float64
	var frac float64 = 0
	var div float64 = 1
	dot := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '.' && !dot:
			dot = true
		case c >= '0' && c <= '9':
			if dot {
				div *= 10
				frac = frac*10 + float64(c-'0')
			} else {
				v = v*10 + float64(c-'0')
			}
		default:
			return 0
		}
	}
	return v + frac/div
}
