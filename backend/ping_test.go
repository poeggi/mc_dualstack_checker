// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestFailure(t *testing.T) {
	dial := func(errno syscall.Errno) error {
		return &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", errno)}
	}
	// The local routing table always has a route to loopback, so a
	// "network unreachable" for it must have come from elsewhere.
	loopback := net.ParseIP("127.0.0.1")
	for _, c := range []struct {
		name, state, reason string
		err                 error
	}{
		{"port closed", "offline", "refused (port closed)", dial(syscall.ECONNREFUSED)},
		{"reset", "offline", "refused (reset)", dial(syscall.ECONNRESET)},
		{"closed", "offline", "refused (closed)", io.ErrUnexpectedEOF},
		{"timeout", "offline", "no response (timeout)", context.DeadlineExceeded},
		{"invalid reply", "offline", "invalid data (bad raknet magic)", probeError("bad raknet magic")},
		{"other", "offline", "failed (unknown error)", errors.New("x")},
		{"host unreachable", "unreachable", "rejected (no route to host)", dial(syscall.EHOSTUNREACH)},
		{"administratively prohibited", "unreachable", "rejected (prohibited)", dial(syscall.EACCES)},
		{"network unreachable, local route exists", "unreachable", "rejected (network unreachable)", dial(syscall.ENETUNREACH)},
		{"no source address", "no_route", "no source address", dial(syscall.EADDRNOTAVAIL)},
	} {
		got := failure(c.err, loopback)
		if got.State != c.state || got.Error != c.reason {
			t.Errorf("%s: %q %q, want %q %q", c.name, got.State, got.Error, c.state, c.reason)
		}
	}
}

func TestFlattenChatDepth(t *testing.T) {
	raw := `"x"`
	for i := 0; i < 1000; i++ {
		raw = `{"text":"a","extra":[` + raw + `]}`
	}
	if got := flattenChat(json.RawMessage(raw), 0); got != strings.Repeat("a", maxChatDepth+1) {
		t.Errorf("flattenChat kept %d levels, want %d", len(got), maxChatDepth+1)
	}
}

func TestClip(t *testing.T) {
	if got := clip("a\u00e4b", 2); got != "a" {
		t.Errorf("clip split a character: %q", got)
	}
	if got := clip("abc", 5); got != "abc" {
		t.Errorf("clip changed a short string: %q", got)
	}
}

func TestLegacyChat(t *testing.T) {
	const s = "SECT"
	for _, c := range []struct{ name, raw, want string }{
		{"plain string keeps its codes", `"SECT6Gold"`, s + "6Gold"},
		{"named colour and bold", `{"text":"Hi","color":"gold","bold":true}`, s + "r" + s + "6" + s + "lHi"},
		{"hex colour", `{"text":"x","color":"#A1b2C3"}`, s + "r" + s + "x" + s + "a" + s + "1" + s + "b" + s + "2" + s + "c" + s + "3x"},
		{"children inherit, siblings reset", `{"text":"a","color":"red","extra":[{"text":"c","color":"blue"},"b",{"text":"d","bold":false}]}`,
			s + "r" + s + "ca" + s + "r" + s + "9c" + s + "r" + s + "cb" + s + "r" + s + "cd"},
		{"invalid colour is dropped", `{"text":"a","color":"#12"}`, s + "ra"},
	} {
		raw := strings.ReplaceAll(c.raw, s, "\u00a7")
		want := strings.ReplaceAll(c.want, s, "\u00a7")
		if got := legacyChat(json.RawMessage(raw), 0, chatStyle{}); got != want {
			t.Errorf("%s: %q, want %q", c.name, got, want)
		}
	}
}

func TestServerIcon(t *testing.T) {
	encode := func(w, h int) string {
		var b bytes.Buffer
		if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
			t.Fatal(err)
		}
		return "data:image/png;base64," + base64.StdEncoding.EncodeToString(b.Bytes())
	}
	good := encode(64, 64)
	if got := serverIcon(good); got != good {
		t.Errorf("valid icon dropped")
	}
	if got := serverIcon(good[:40] + "\n" + good[40:]); got != good {
		t.Errorf("icon with a line break not joined")
	}
	for name, s := range map[string]string{
		"wrong size":  encode(32, 32),
		"not png":     "data:image/jpeg;base64," + good[len("data:image/png;base64,"):],
		"bad base64":  "data:image/png;base64,***",
		"too large":   good + strings.Repeat("A", iconMax),
		"no data url": "https://example.com/icon.png",
	} {
		if got := serverIcon(s); got != "" {
			t.Errorf("%s: icon passed on", name)
		}
	}
}

func TestParsePongPorts(t *testing.T) {
	payload := "MCPE;\u00a7bHello;766;1.21.50;3;20;123;\u00a7aWorld;Survival;1;19132;19133;"
	b := []byte{raknetUnconnectedPong}
	b = binary.BigEndian.AppendUint64(b, 1)
	b = binary.BigEndian.AppendUint64(b, 2)
	b = append(b, raknetMagic...)
	b = binary.BigEndian.AppendUint16(b, uint16(len(payload)))
	b = append(b, payload...)
	info, err := parsePong(b)
	if err != nil {
		t.Fatal(err)
	}
	if info.Port4 != 19132 || info.Port6 != 19133 {
		t.Errorf("announced ports %d %d, want 19132 19133", info.Port4, info.Port6)
	}
	if info.MOTD != "Hello" || info.MOTDRaw != "\u00a7bHello" || info.MapRaw != "\u00a7aWorld" {
		t.Errorf("motd %q raw %q map raw %q", info.MOTD, info.MOTDRaw, info.MapRaw)
	}
}

func TestEditionError(t *testing.T) {
	if editionError != "edition must be bedrock or java" {
		t.Errorf("editionError = %q", editionError)
	}
}

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
