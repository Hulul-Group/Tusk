package api

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// This file renders Kutt's JSON shapes (utils.js sanitize.*) with the same
// key order as Express (JSON.stringify of the spread DB row), JavaScript
// Date serialisation (ISO-8601, milliseconds, "Z") and no HTML escaping
// (JSON.stringify does not escape <, >, &).

type field struct {
	k string
	v any
}

// obj is an ordered JSON object.
type obj []field

func (o obj) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, f := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := marshal(f.k)
		b.Write(kb)
		b.WriteByte(':')
		vb, err := marshal(f.v)
		if err != nil {
			return nil, err
		}
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// marshal is json.Marshal without HTML escaping.
func marshal(v any) ([]byte, error) {
	if o, ok := v.(obj); ok {
		return o.MarshalJSON()
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// jsDate is a time serialised like JavaScript's Date#toJSON.
type jsDate time.Time

func (d jsDate) MarshalJSON() ([]byte, error) {
	return []byte(`"` + isoMS(time.Time(d)) + `"`), nil
}

func isoMS(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func dateOrNil(t *time.Time) any {
	if t == nil {
		return nil
	}
	return jsDate(*t)
}

func strOrNil(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func int64OrNil(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// localeInt is Number#toLocaleString("en-US") for integers.
func localeInt(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// timeAgo ports utils.getTimeAgo.
func timeAgo(t, now time.Time) string {
	const minute, hour, day = 60, 3600, 86400
	const week, month, year = day * 7, day * 30, day * 365
	secs := int64(jsRound(float64(now.Sub(t).Milliseconds()) / 1000))
	if secs < minute {
		s := strconv.FormatInt(secs, 10) + " second"
		if secs != 1 {
			s += "s"
		}
		return s + " ago"
	}
	var div int64
	var unit string
	switch {
	case secs < hour:
		div, unit = minute, "minute"
	case secs < day:
		div, unit = hour, "hour"
	case secs < week:
		div, unit = day, "day"
	case secs < month:
		div, unit = week, "week"
	case secs < year:
		div, unit = month, "month"
	default:
		div, unit = year, "year"
	}
	c := secs / div
	s := strconv.FormatInt(c, 10) + " " + unit
	if c > 1 {
		s += "s"
	}
	return s + " ago"
}
