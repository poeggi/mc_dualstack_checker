// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSketchEstimate(t *testing.T) {
	u := newUsage()
	for _, n := range []int{1, 14, 500, 20000} {
		s := newSketch()
		for i := 0; i < n; i++ {
			s.add(u.hash(fmt.Sprintf("198.51.%d.%d", i/256, i%256)))
			s.add(u.hash(fmt.Sprintf("198.51.%d.%d", i/256, i%256)))
		}
		got := float64(s.estimate())
		if d := got/float64(n) - 1; d > 0.05 || d < -0.05 {
			t.Errorf("%d clients estimated as %.0f", n, got)
		}
	}
}

func TestUsageRoll(t *testing.T) {
	u := newUsage()
	t0 := time.Date(2026, 9, 25, 12, 0, 30, 0, time.UTC)
	u.add(t0, sample{false, false, "198.51.100.1"})
	u.add(t0, sample{false, false, "198.51.100.1"})
	u.add(t0, sample{true, false, "2001:db8::/64"})
	clear(u.rolled)

	u.roll(t0.Add(time.Minute))
	m := u.Series["minutes"].Done
	if len(m) != 1 || m[0].IPv4 != (counts{2, 1}) || m[0].IPv6 != (counts{1, 1}) {
		t.Fatalf("finished minute %+v", m)
	}
	if !u.rolled["minutes"] || u.rolled["hours"] || len(u.Series["hours"].Done) != 0 {
		t.Fatalf("rolled %v, hours %+v", u.rolled, u.Series["hours"].Done)
	}

	u.add(t0.Add(3*time.Minute), sample{false, false, "198.51.100.2"})
	m = u.Series["minutes"].Done
	if len(m) != 3 || m[0].IPv4.Requests != 0 || m[2].IPv4 != (counts{2, 1}) {
		t.Fatalf("minutes after 3 min: %+v", m)
	}

	u.roll(t0.Add(5 * time.Hour))
	h := u.Series["hours"].Done
	if len(u.Series["minutes"].Done) != 60 || len(h) != 5 || h[4].IPv4 != (counts{3, 2}) {
		t.Fatalf("after 5 h: %d minutes, hours %+v", len(u.Series["minutes"].Done), h)
	}
}

func TestUsageCacheSplit(t *testing.T) {
	u := newUsage()
	t0 := time.Date(2026, 9, 25, 12, 0, 30, 0, time.UTC)
	u.add(t0, sample{false, false, "198.51.100.1"})
	u.add(t0, sample{false, true, "198.51.100.1"})
	u.add(t0, sample{false, true, "198.51.100.2"})
	u.add(t0, sample{true, false, "2001:db8::/64"})

	u.roll(t0.Add(time.Minute))
	m := u.Series["minutes"].Done[0]
	if m.Fresh == nil || m.Cached == nil || *m.Fresh != (counts{2, 2}) || *m.Cached != (counts{2, 1}) {
		t.Fatalf("finished minute %+v, fresh %+v, cached %+v", m, m.Fresh, m.Cached)
	}
	if !strings.Contains(string(u.public("minutes", t0)), `"fresh":{"requests":2,"clients":2},"cached":{"requests":2,"clients":1}`) {
		t.Fatalf("published %s", u.public("minutes", t0))
	}

	u.roll(t0.Add(3 * time.Minute))
	if e := u.Series["minutes"].Done[0]; e.Fresh == nil || *e.Fresh != (counts{}) || *e.Cached != (counts{}) {
		t.Fatalf("idle minute %+v", e)
	}
}

// State kept by a backend that did not split by cache use: the running
// periods are not split, the next ones are.
func TestUsageCacheSplitAfterUpgrade(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, statsPublic), 0o755); err != nil {
		t.Fatal(err)
	}
	u := newUsage()
	t0 := time.Now().UTC().Truncate(time.Minute)
	u.add(t0, sample{false, false, "198.51.100.1"})
	for _, s := range u.Series {
		s.Fresh, s.Cur.Fresh, s.Cur.Cached = nil, nil, nil
	}
	u.publish(dir, t0)

	v := loadUsage(dir)
	if s := v.Series["minutes"]; len(s.Fresh) != hllM || s.Cur.Fresh != nil || s.Cur.Cached != nil {
		t.Fatalf("reloaded minutes %+v", s.Cur)
	}
	v.add(t0, sample{false, true, "198.51.100.2"})
	v.add(t0.Add(time.Minute), sample{false, true, "198.51.100.2"})
	m := v.Series["minutes"].Done[0]
	if m.IPv4 != (counts{2, 2}) || m.Fresh != nil || m.Cached != nil {
		t.Fatalf("unsplit minute %+v", m)
	}
	if strings.Contains(string(v.public("minutes", t0)), "fresh") {
		t.Fatalf("unsplit minute published with split: %s", v.public("minutes", t0))
	}
	v.roll(t0.Add(2 * time.Minute))
	if m := v.Series["minutes"].Done[0]; m.Cached == nil || *m.Cached != (counts{1, 1}) || *m.Fresh != (counts{}) {
		t.Fatalf("split minute %+v", m)
	}
}

func TestUsageGapAfterDowntime(t *testing.T) {
	u := newUsage()
	t0 := time.Date(2026, 9, 25, 0, 0, 30, 0, time.UTC)
	for i := 0; i < 72; i++ {
		u.add(t0.Add(time.Duration(i)*time.Hour), sample{false, false, "198.51.100.1"})
	}
	last := t0.Add(71 * time.Hour)

	now := last.Add(3 * time.Hour)
	u.roll(now)
	h := u.Series["hours"].Done
	if len(h) != 48 || !h[0].Start.Equal(now.Truncate(time.Hour).Add(-time.Hour)) ||
		h[0].IPv4.Requests != 0 || h[1].IPv4.Requests != 0 || h[2].IPv4.Requests != 1 {
		t.Fatalf("hours after 2 idle hours: %+v", h[:3])
	}

	now = last.Add(40 * 24 * time.Hour)
	u.roll(now)
	d := u.Series["days"].Done
	want := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
	if len(d) != 30 || !d[0].Start.Equal(want) || d[29].IPv4.Requests != 0 {
		t.Fatalf("days after 40 idle days: newest %v, want %v", d[0].Start, want)
	}
	for i := 1; i < len(d); i++ {
		if !d[i].Start.Equal(d[i-1].Start.AddDate(0, 0, -1)) {
			t.Fatalf("days not consecutive at %d: %v, %v", i, d[i-1].Start, d[i].Start)
		}
	}
}

func TestUsageSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, statsPublic), 0o755); err != nil {
		t.Fatal(err)
	}
	u := newUsage()
	now := time.Now()
	u.add(now, sample{false, false, "198.51.100.1"})
	u.publish(dir, now)

	v := loadUsage(dir)
	if v.Salt != u.Salt || v.Series["days"].Cur.IPv4.Requests != 1 {
		t.Fatalf("reloaded: salt %v, days %+v", v.Salt == u.Salt, v.Series["days"].Cur)
	}
	files, _ := os.ReadDir(filepath.Join(dir, statsPublic))
	if len(files) != len(periodKinds) {
		t.Fatalf("published files: %v", files)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, statsPublic, "months.json")); !strings.Contains(string(b), `"periods":[]`) {
		t.Fatalf("no finished month is not an empty list: %s", b)
	}

	// Without new requests the state is not written again.
	os.Remove(filepath.Join(dir, statsPrivate))
	u.publish(dir, now)
	if _, err := os.Stat(filepath.Join(dir, statsPrivate)); err == nil {
		t.Fatal("state written without new requests")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("files left in the directory: %v", entries)
	}
}
