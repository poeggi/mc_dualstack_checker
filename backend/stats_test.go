// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

type statsDoc map[string][]bucket

func publicDoc(t *testing.T, u *usage, now time.Time) statsDoc {
	t.Helper()
	var d statsDoc
	u.roll(now)
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(u.public(now), &raw); err != nil {
		t.Fatal(err)
	}
	d = statsDoc{}
	for _, k := range periodKinds {
		var b []bucket
		if err := json.Unmarshal(raw[k.name], &b); err != nil {
			t.Fatal(err)
		}
		d[k.name] = b
	}
	return d
}

func TestUsageRoll(t *testing.T) {
	u := newUsage()
	t0 := time.Date(2026, 9, 25, 12, 0, 30, 0, time.UTC)
	u.add(t0, sample{false, "198.51.100.1"})
	u.add(t0, sample{false, "198.51.100.1"})
	u.add(t0, sample{true, "2001:db8::/64"})

	d := publicDoc(t, u, t0)
	cur := d["minutes"][0]
	if !cur.Current || cur.IPv4 != (counts{2, 1}) || cur.IPv6 != (counts{1, 1}) {
		t.Fatalf("running minute %+v", cur)
	}

	u.add(t0.Add(3*time.Minute), sample{false, "198.51.100.2"})
	d = publicDoc(t, u, t0.Add(3*time.Minute))
	m := d["minutes"]
	if len(m) != 4 || !m[0].Current || m[1].IPv4.Requests != 0 || m[3].IPv4 != (counts{2, 1}) {
		t.Fatalf("minutes after 3 min: %+v", m)
	}
	if h := d["hours"]; len(h) != 1 || h[0].IPv4 != (counts{3, 2}) {
		t.Fatalf("hours: %+v", h)
	}

	d = publicDoc(t, u, t0.Add(5*time.Hour))
	if len(d["minutes"]) != 60 || len(d["hours"]) != 6 {
		t.Fatalf("after 5 h: %d minutes, %d hours", len(d["minutes"]), len(d["hours"]))
	}
}

func TestUsageSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	u := newUsage()
	now := time.Now()
	u.add(now, sample{false, "198.51.100.1"})
	u.publish(dir, now)
	u.publish(dir, now)

	v := loadUsage(dir)
	if v.Salt != u.Salt || v.Series["days"].Cur.IPv4.Requests != 1 {
		t.Fatalf("reloaded: salt %v, days %+v", v.Salt == u.Salt, v.Series["days"].Cur)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("files left in the directory: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(dir, statsPublic)); err != nil {
		t.Fatal(err)
	}
}
