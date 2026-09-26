// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const fakeStatus = `{"version":{"name":"26.3","protocol":777},"players":{"online":1,"max":20},"description":"hi"}`

// fakeJava serves one connection on loopback: it reads the handshake and the
// status request, then hands the connection and the handshake host to serve.
func fakeJava(t *testing.T, serve func(c net.Conn, r *bufio.Reader, host string)) target {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		if host, err := readHandshake(r); err == nil {
			serve(c, r, host)
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return target{ip: addr.IP, port: addr.Port}
}

// readHandshake reads a handshake and a status request and returns the
// handshake host.
func readHandshake(r *bufio.Reader) (string, error) {
	if _, err := readVarint(r); err != nil {
		return "", err
	}
	if id, err := r.ReadByte(); err != nil || id != 0x00 {
		return "", errors.New("not a handshake")
	}
	if _, err := readVarint(r); err != nil {
		return "", err
	}
	n, err := readVarint(r)
	if err != nil {
		return "", err
	}
	host := make([]byte, n)
	if _, err := io.ReadFull(r, host); err != nil {
		return "", err
	}
	// Port, next state, then the status request: length 1, id 0.
	if _, err := r.Discard(2 + 1 + 2); err != nil {
		return "", err
	}
	return string(host), nil
}

func writeStatus(c net.Conn, status string) {
	body := appendVarint([]byte{0x00}, len(status))
	body = append(body, status...)
	_, _ = c.Write(append(appendVarint(nil, len(body)), body...))
}

// answerPing reads a Ping and sends it back: a Pong has the same length,
// packet id and payload.
func answerPing(c net.Conn, r *bufio.Reader) {
	b := make([]byte, 10)
	if _, err := io.ReadFull(r, b); err == nil {
		_, _ = c.Write(b)
	}
}

// The status comes late and the Pong at once: the round trip is the time
// from Ping to Pong.
func TestPingJavaRTT(t *testing.T) {
	tg := fakeJava(t, func(c net.Conn, r *bufio.Reader, _ string) {
		time.Sleep(300 * time.Millisecond)
		writeStatus(c, fakeStatus)
		answerPing(c, r)
	})
	info, rtt, err := pingJava(context.Background(), "tcp4", tg)
	if err != nil {
		t.Fatal(err)
	}
	if info.Version != "26.3" || rtt >= 200*time.Millisecond {
		t.Errorf("version %q, rtt %v; want 26.3 and less than 200ms", info.Version, rtt)
	}
}

// Without a matching Pong the round trip is the whole exchange, and the Pong
// is waited for at most pongWait.
func TestPingJavaNoPong(t *testing.T) {
	for name, after := range map[string]func(net.Conn, *bufio.Reader){
		"close":   func(net.Conn, *bufio.Reader) {},
		"silence": func(net.Conn, *bufio.Reader) { time.Sleep(2 * pongWait) },
		"other payload": func(c net.Conn, r *bufio.Reader) {
			b := make([]byte, 10)
			if _, err := io.ReadFull(r, b); err == nil {
				b[9] ^= 1
				_, _ = c.Write(b)
			}
		},
	} {
		tg := fakeJava(t, func(c net.Conn, r *bufio.Reader, _ string) {
			time.Sleep(300 * time.Millisecond)
			writeStatus(c, fakeStatus)
			after(c, r)
		})
		start := time.Now()
		_, rtt, err := pingJava(context.Background(), "tcp4", tg)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if rtt < 300*time.Millisecond {
			t.Errorf("%s: rtt %v, want the whole exchange", name, rtt)
		}
		if took := time.Since(start); took > 300*time.Millisecond+pongWait+500*time.Millisecond {
			t.Errorf("%s: probe took %v", name, took)
		}
	}
}

func TestPingJavaID(t *testing.T) {
	hosts := make(chan string, 1)
	tg := fakeJava(t, func(c net.Conn, _ *bufio.Reader, host string) {
		hosts <- host
		writeStatus(c, fakeStatus)
	})
	tg.host, tg.id = "mc.example.net", "team a&b"
	if _, _, err := pingJava(context.Background(), "tcp4", tg); err != nil {
		t.Fatal(err)
	}
	if got := <-hosts; got != "mc.example.net?_id=team+a%26b" {
		t.Errorf("handshake host %q", got)
	}
}

// Vanilla closes after the handshake when status is off or the ID does not
// match.
func TestPingJavaNoStatus(t *testing.T) {
	for _, c := range []struct {
		id    string
		reset bool
		want  string
	}{
		{"", false, "connected, no status (status disabled or connection ID required)"},
		{"x", false, "connected, no status (status disabled or wrong connection ID)"},
		{"", true, "connected, no status (status disabled or connection ID required)"},
	} {
		if c.reset && runtime.GOOS != "linux" {
			continue // other systems report a reset with other errors
		}
		tg := fakeJava(t, func(conn net.Conn, _ *bufio.Reader, _ string) {
			if c.reset {
				_ = conn.(*net.TCPConn).SetLinger(0)
			}
		})
		tg.id = c.id
		_, _, err := pingJava(context.Background(), "tcp4", tg)
		if got := failure(err, tg.ip); got.State != "offline" || got.Error != c.want {
			t.Errorf("id %q reset %v: %q %q, want offline %q", c.id, c.reset, got.State, got.Error, c.want)
		}
	}
}

func TestParseJavaStatus(t *testing.T) {
	sect := string(rune(0xa7))
	counts := func(i *ServerInfo) string {
		show := func(p *int) string {
			if p == nil {
				return "-"
			}
			return strconv.Itoa(*p)
		}
		return show(i.PlayersOnline) + "/" + show(i.PlayersMax)
	}
	for _, c := range []struct {
		name, raw                                 string
		motd, motdRaw, version, protocol, contact string
		players                                   string
	}{
		{"classic", `{"version":{"name":"1.21.4","protocol":769},"players":{"online":3,"max":20},"description":{"text":"Hi"}}`,
			"Hi", sect + "rHi", "1.21.4", "769", "", "3/20"},
		{"top-level array", `{"version":{"name":"v"},"description":[{"text":"a","color":"red"},"b",{"text":"c","color":"blue"}]}`,
			"abc", sect + "r" + sect + "ca" + sect + "r" + sect + "cb" + sect + "r" + sect + "9c", "v", "", "", "-/-"},
		{"array in extra", `{"description":{"text":"x","extra":[["y",{"text":"z"}]]}}`,
			"xyz", sect + "rx" + sect + "ry" + sect + "rz", "", "", "", "-/-"},
		{"translate with fallback", `{"description":{"translate":"motd.key","fallback":"Welcome"}}`,
			"Welcome", sect + "rWelcome", "", "", "", "-/-"},
		{"translate without fallback", `{"version":{"name":"v"},"description":{"translate":"motd.key","extra":["!"]}}`,
			"!", sect + "r!", "v", "", "", "-/-"},
		{"component without text", `{"description":{"text":"","extra":[{"object":"atlas","sprite":"item/apple"},"x"]}}`,
			"x", sect + "rx", "", "", "", "-/-"},
		{"fields of the wrong type", `{"version":{"name":7,"protocol":"47"},"players":{"online":"2","max":true},"description":{"text":"ok","bold":"yes","extra":{"text":"no"}}}`,
			"ok", sect + "rok", "", "47", "", "2/-"},
		{"coloured version, contact", `{"version":{"name":"` + sect + `cMaintenance","protocol":-1},"players":"many","description":"m","contact":" ops@example.com "}`,
			"m", "", "Maintenance", "-1", "ops@example.com", "-/-"},
	} {
		info, err := parseJavaStatus([]byte(c.raw))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		got := []string{info.MOTD, info.MOTDRaw, info.Version, info.Protocol, info.Contact, counts(info)}
		want := []string{c.motd, c.motdRaw, c.version, c.protocol, c.contact, c.players}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("%s: %q, want %q", c.name, got, want)
		}
	}
	for name, raw := range map[string]string{
		"not an object": `[{"description":"x"}]`,
		"no text":       `{"players":{"online":1,"max":2}}`,
	} {
		if _, err := parseJavaStatus([]byte(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The player sample keeps at most sampleMax cleaned names; the secure chat
// rule is passed on only as a boolean.
func TestParseJavaStatusSample(t *testing.T) {
	sample := `{"name":"` + string(rune(0xa7)) + `cAlice","id":"1"},{"name":"  "},"bob",{"name":7},{"name":"Carol\u0000"}`
	for i := 0; i < sampleMax; i++ {
		sample += `,{"name":"p` + strconv.Itoa(i) + `"}`
	}
	info, err := parseJavaStatus([]byte(`{"version":{"name":"v"},"players":{"online":1,"max":2,"sample":[` + sample + `]},"enforcesSecureChat":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(info.PlayerSample, ","); len(info.PlayerSample) != sampleMax || !strings.HasPrefix(got, "Alice,Carol,p0,") {
		t.Errorf("sample %q", info.PlayerSample)
	}
	if info.EnforcesSecureChat == nil || !*info.EnforcesSecureChat {
		t.Errorf("secure chat %v, want true", info.EnforcesSecureChat)
	}
	info, _ = parseJavaStatus([]byte(`{"version":{"name":"v"},"players":{"sample":"none"},"enforcesSecureChat":"yes"}`))
	if info.PlayerSample != nil || info.EnforcesSecureChat != nil {
		t.Errorf("sample %v, secure chat %v, want none", info.PlayerSample, info.EnforcesSecureChat)
	}
}

func TestFormEscape(t *testing.T) {
	for in, want := range map[string]string{
		"team a&b":       "team+a%26b",
		"A.z-0*_~?=%/@,": "A.z-0*_%7E%3F%3D%25%2F%40%2C",
	} {
		if got := formEscape(in); got != want {
			t.Errorf("formEscape(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConnectionID(t *testing.T) {
	for id, want := range map[string]bool{
		"a":                     true,
		"team a":                true,
		"x!~?&=@":               true,
		strings.Repeat("a", 64): true,
		"":                      false,
		" a":                    false,
		"a ":                    false,
		"a,b":                   false,
		strings.Repeat("a", 65): false,
		"a" + string(rune(9)):   false,
		string(rune(0xe4)):      false,
	} {
		if got := connectionID(id); got != want {
			t.Errorf("connectionID(%q) = %v, want %v", id, got, want)
		}
	}
}
