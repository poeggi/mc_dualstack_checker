// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestHostName(t *testing.T) {
	for in, want := range map[string]string{
		"Play_X.Example-1.net.":          "play_x.example-1.net",
		"a b.example":                    "",
		"ex!ample.net":                   "",
		"a..example":                     "",
		strings.Repeat("a", 64) + ".net": "",
	} {
		if got := hostName(in); got != want {
			t.Errorf("hostName(%q) = %q, want %q", in, got, want)
		}
	}
}

// A resolver that ignores its deadline is waited for until srvWait: an
// answer after srvTimeout still counts, a later one is not waited for.
func TestLookupSRVWait(t *testing.T) {
	defer func(r func(context.Context, string, string, string) (string, []*net.SRV, error)) { srvResolver = r }(srvResolver)
	answerAfter := func(d time.Duration) func(context.Context, string, string, string) (string, []*net.SRV, error) {
		return func(context.Context, string, string, string) (string, []*net.SRV, error) {
			time.Sleep(d)
			return "", []*net.SRV{{Target: "mc.example.net.", Port: 25577}}, nil
		}
	}

	srvResolver = answerAfter(srvTimeout + 200*time.Millisecond)
	if got := lookupSRV(context.Background(), "minecraft", "tcp", "play.example.net"); got == nil {
		t.Errorf("answer before srvWait dropped")
	}

	srvResolver = answerAfter(10 * time.Second)
	start := time.Now()
	if got := lookupSRV(context.Background(), "minecraft", "tcp", "play.example.net"); got != nil {
		t.Errorf("answer after srvWait used: %+v", got)
	}
	if waited := time.Since(start); waited < srvWait || waited > srvWait+time.Second {
		t.Errorf("waited %v, want about %v", waited, srvWait)
	}
}

func TestSRVTarget(t *testing.T) {
	got := srvTarget([]*net.SRV{{Target: ".", Port: 25565}, {Target: "mc.example.net.", Port: 0}, {Target: "MC.Example.net.", Port: 25577}})
	if got == nil || got.Host != "mc.example.net" || got.Port != 25577 {
		t.Errorf("srvTarget = %+v, want mc.example.net:25577", got)
	}
	if got := srvTarget([]*net.SRV{{Target: "play_x.example.net.", Port: 25565}}); got == nil || got.Host != "play_x.example.net" {
		t.Errorf("target with an underscore dropped: %+v", got)
	}
	if srvTarget(nil) != nil {
		t.Errorf("no records gave a target")
	}
}
