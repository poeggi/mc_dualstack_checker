// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"net"
	"testing"
	"time"
)

// fakeTransport ends after d with a status, a weak answer or an error.
func fakeTransport(name string, d time.Duration, kind string) transport {
	return transport{name: name, network: "tcp", probe: func(ctx context.Context, _ string, _ target) (*ServerInfo, time.Duration, error) {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		}
		switch kind {
		case "status":
			return &ServerInfo{MOTD: name}, d, nil
		case "weak":
			return &ServerInfo{weak: true}, d, nil
		}
		return nil, 0, probeError(name + " failed")
	}}
}

func TestRace(t *testing.T) {
	const ms = time.Millisecond
	for _, c := range []struct {
		name        string
		a, b        transport
		want        string // the transport that answers, "" for a failure
		motd        string
		early, late time.Duration
	}{
		{"preferred answers first", fakeTransport("a", 50*ms, "status"), fakeTransport("b", 10*ms, "status"), "a", "a", 40 * ms, 150 * ms},
		{"preferred wins within grace", fakeTransport("a", 400*ms, "status"), fakeTransport("b", 10*ms, "status"), "a", "a", 390 * ms, 500 * ms},
		{"grace ends", fakeTransport("a", 3*time.Second, "status"), fakeTransport("b", 10*ms, "status"), "b", "b", 740 * ms, 900 * ms},
		{"next starts when the first fails", fakeTransport("a", 20*ms, "error"), fakeTransport("b", 10*ms, "status"), "b", "b", 25 * ms, 150 * ms},
		{"weak loses to a status", fakeTransport("a", 20*ms, "weak"), fakeTransport("b", 10*ms, "status"), "b", "b", 25 * ms, 150 * ms},
		{"weak alone", fakeTransport("a", 20*ms, "weak"), fakeTransport("b", 10*ms, "error"), "a", "", 25 * ms, 150 * ms},
		{"weak waits grace at most", fakeTransport("a", 20*ms, "weak"), fakeTransport("b", 3*time.Second, "error"), "a", "", 510 * ms, 650 * ms},
		{"late status beats weak", fakeTransport("a", 20*ms, "weak"), fakeTransport("b", 300*ms, "status"), "b", "b", 310 * ms, 450 * ms},
		{"all fail", fakeTransport("a", 20*ms, "error"), fakeTransport("b", 30*ms, "error"), "", "", 45 * ms, 150 * ms},
	} {
		ed := edition{transports: []transport{c.a, c.b}}
		start := time.Now()
		res := ping(context.Background(), ed, target{ip: net.ParseIP("127.0.0.1"), port: 1})
		took := time.Since(start)
		if took < c.early || took > c.late {
			t.Errorf("%s: took %v, want %v to %v", c.name, took, c.early, c.late)
		}
		if c.want == "" {
			if res.State != "offline" || res.Error != "invalid data (b failed)" || len(res.Errors) != 2 || res.Errors["a"] != "invalid data (a failed)" {
				t.Errorf("%s: %+v", c.name, res)
			}
			continue
		}
		if res.State != "online" || res.Info.Transport != c.want || res.Info.MOTD != c.motd {
			t.Errorf("%s: %+v %+v, want transport %q motd %q", c.name, res, res.Info, c.want, c.motd)
		}
	}
}
