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
	u.add(t0, sample{false, "198.51.100.1"})
	u.add(t0, sample{false, "198.51.100.1"})
	u.add(t0, sample{true, "2001:db8::/64"})
	clear(u.rolled)

	u.roll(t0.Add(time.Minute))
	m := u.Series["minutes"].Done
	if len(m) != 1 || m[0].IPv4 != (counts{2, 1}) || m[0].IPv6 != (counts{1, 1}) {
		t.Fatalf("finished minute %+v", m)
	}
	if !u.rolled["minutes"] || u.rolled["hours"] || len(u.Series["hours"].Done) != 0 {
		t.Fatalf("rolled %v, hours %+v", u.rolled, u.Series["hours"].Done)
	}

	u.add(t0.Add(3*time.Minute), sample{false, "198.51.100.2"})
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

func TestUsageSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, statsPublic), 0o755); err != nil {
		t.Fatal(err)
	}
	u := newUsage()
	now := time.Now()
	u.add(now, sample{false, "198.51.100.1"})
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
