// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"testing"
	"time"
)

func TestSystemsWindow(t *testing.T) {
	const ip = "198.51.100.1"
	for _, tc := range []struct {
		oldestAge time.Duration
		min, max  int
	}{
		{0, 60, 61},
		{30 * time.Second, 30, 31},
		// The oldest leaves the window in 2 s; the block still lasts 7 s.
		{58 * time.Second, 7, 8},
	} {
		l := &limiter{clients: map[string]*client{}}
		for i := 0; i < systemsMax; i++ {
			if wait, _ := l.admitProbe(ip, fmt.Sprint("sys", i)); wait != 0 {
				t.Fatalf("system %d refused", i)
			}
		}
		l.clients[ip].systems["sys0"] = time.Now().Add(-tc.oldestAge)
		wait, reason := l.admitProbe(ip, "new")
		if reason != "systems" || wait < tc.min || wait > tc.max {
			t.Errorf("oldest %v old: wait %d (%s), want %d-%d s", tc.oldestAge, wait, reason, tc.min, tc.max)
		}
	}
}

func TestSystemsWindowSlides(t *testing.T) {
	const ip = "198.51.100.1"
	l := &limiter{clients: map[string]*client{}}
	for i := 0; i < systemsMax; i++ {
		l.admitProbe(ip, fmt.Sprint("sys", i))
	}
	l.clients[ip].systems["sys0"] = time.Now().Add(-systemsWindow - time.Second)
	if wait, reason := l.admitProbe(ip, "new"); wait != 0 {
		t.Errorf("new system after the oldest left: wait %d (%s)", wait, reason)
	}
}
