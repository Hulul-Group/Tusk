package sqlstore

import (
	"testing"
	"time"
)

func d(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t
}

// Expected values computed with date-fns 2.30.0 (node, TZ=UTC) — see testdata/datefns_golden.js.
func TestDateFns(t *testing.T) {
	cases := []struct {
		l, r        string
		hours, days int
		months      int
	}{
		{"2026-03-31T12:30:00Z", "2026-03-31T12:00:00Z", 0, 0, 0},
		{"2026-03-31T12:30:00Z", "2026-03-30T12:30:00.001Z", 23, 0, 0},
		{"2026-03-31T12:30:00Z", "2026-03-28T13:00:00Z", 71, 2, 0},
		{"2026-03-31T12:30:00Z", "2026-02-28T12:30:00Z", 744, 31, 1},
		{"2026-03-31T12:30:00Z", "2026-02-28T13:30:00Z", 743, 30, 1},
		{"2026-02-28T00:00:00Z", "2026-01-31T00:00:00Z", 672, 28, 1},
		{"2026-03-30T00:00:00Z", "2026-01-31T00:00:00Z", 1392, 58, 1},
		{"2026-03-31T12:30:00Z", "2025-03-31T12:30:01Z", 8759, 364, 11},
		{"2024-02-29T10:00:00Z", "2023-11-30T11:00:00Z", 2183, 90, 3},
	}
	for _, c := range cases {
		l, r := d(c.l), d(c.r)
		if got := differenceInHours(l, r); got != c.hours {
			t.Errorf("hours(%s,%s)=%d want %d", c.l, c.r, got, c.hours)
		}
		if got := differenceInDays(l, r); got != c.days {
			t.Errorf("days(%s,%s)=%d want %d", c.l, c.r, got, c.days)
		}
		if got := differenceInMonths(l, r); got != c.months {
			t.Errorf("months(%s,%s)=%d want %d", c.l, c.r, got, c.months)
		}
	}
	if got := subMonths(d("2026-03-31T00:00:00Z"), 1); !got.Equal(d("2026-02-28T00:00:00Z")) {
		t.Error("subMonths clamp", got)
	}
}

func TestOrderedCounts(t *testing.T) {
	o, err := parseCounts([]byte(`{"b":1,"10":2,"a":3,"2":4}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := o.json(); got != `{"2":4,"10":2,"b":1,"a":3}` {
		t.Fatal(got)
	}
	mergeCounts(&o, map[string]int64{"a": 1, "z": 5})
	if got := o.json(); got != `{"2":4,"10":2,"b":1,"a":4,"z":5}` {
		t.Fatal(got)
	}
	arr := countsToArray(&o)
	if arr[0].Name != "z" || arr[1].Name != "2" || arr[2].Name != "a" {
		t.Fatalf("%+v", arr)
	}
	if o2, _ := parseCounts([]byte(`"{\"x\":1}"`)); o2.vals["x"] != 1 {
		t.Fatal("double-encoded")
	}
	if o3, _ := parseCounts(nil); len(o3.keys) != 0 {
		t.Fatal("nil")
	}
}

func TestRebind(t *testing.T) {
	s := &DB{d: dPostgres}
	if got := s.q("a = ? AND b = '?' AND c = ?"); got != "a = $1 AND b = '?' AND c = $2" {
		t.Fatal(got)
	}
	if v, ok := jsParseInt(" 12abc"); !ok || v != 12 {
		t.Fatal("parseInt")
	}
	if _, ok := jsParseInt("abc"); ok {
		t.Fatal("NaN")
	}
}
