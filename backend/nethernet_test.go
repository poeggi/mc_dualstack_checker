// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

const joinStatus = `{"name":"Dedicated Server","protocol":2193,"version":"1.26.52","level":"Bedrock level","players":1,"maxPlayers":10,"gameType":1}`

// joinTarget is the loopback address of srv.
func joinTarget(t *testing.T, addr string) target {
	t.Helper()
	a, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	return target{ip: a.IP, port: a.Port}
}

func joinHandler(status int, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/join" || r.UserAgent() != netherNetAgent {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
}

func TestPingNetherNet(t *testing.T) {
	for _, c := range []struct {
		name, body string
		status     int
		want       string // motd|version|protocol|map|players|gamemode, "weak", or the error
	}{
		{"status", joinStatus, 200, "Dedicated Server|1.26.52|2193|Bedrock level|1/10|Creative"},
		{"protocol as a string", `{"name":"x","protocol":"2216","version":"1.26.60","gameType":7}`, 200, "x|1.26.60|2216||-/-|7"},
		{"empty body", "", 200, "weak"},
		{"not found", "", 404, "invalid data (HTTP 404)"},
		{"web page", "<html>hello</html>", 200, "invalid data (not a NetherNet status)"},
		{"json without status", `{"ok":true}`, 200, "invalid data (not a NetherNet status)"},
		{"too large", `{"name":"` + strings.Repeat("a", netherNetMaxBody) + `"}`, 200, "invalid data (status too large)"},
	} {
		srv := httptest.NewServer(joinHandler(c.status, c.body))
		tg := joinTarget(t, srv.Listener.Addr().String())
		info, _, err := pingNetherNet(context.Background(), "tcp4", tg)
		srv.Close()
		if got := describeJoin(info, err, tg); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// describeJoin sums up a NetherNet probe for comparison.
func describeJoin(info *ServerInfo, err error, tg target) string {
	if err != nil {
		return failure(err, tg.ip).Error
	}
	if info.weak {
		return "weak"
	}
	show := func(p *int) string {
		if p == nil {
			return "-"
		}
		return strconv.Itoa(*p)
	}
	return strings.Join([]string{info.MOTD, info.Version, info.Protocol, info.Map,
		show(info.PlayersOnline) + "/" + show(info.PlayersMax), info.Gamemode}, "|")
}

// Servers that require HTTPS get the request again over TLS: one answers
// plain HTTP with 400, the other closes on it.
func TestPingNetherNetTLS(t *testing.T) {
	answers := httptest.NewTLSServer(joinHandler(200, joinStatus))
	defer answers.Close()

	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closes := &http.Server{Handler: joinHandler(200, joinStatus)}
	defer closes.Close()
	go closes.Serve(tlsOnly{ln, answers.TLS})

	for name, addr := range map[string]string{
		"answers plain HTTP with 400": answers.Listener.Addr().String(),
		"closes on plain HTTP":        ln.Addr().String(),
	} {
		tg := joinTarget(t, addr)
		tg.host = "mc.example.net"
		info, _, err := pingNetherNet(context.Background(), "tcp4", tg)
		if err != nil || info.MOTD != "Dedicated Server" {
			t.Errorf("%s: %+v %v", name, info, err)
		}
	}
}

// tlsOnly accepts TLS connections only. Its connections are not *tls.Conn,
// so the HTTP server does not answer plain HTTP with a 400 but closes.
type tlsOnly struct {
	net.Listener
	cfg *tls.Config
}

type quietConn struct{ net.Conn }

func (l tlsOnly) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return quietConn{tls.Server(c, l.cfg)}, nil
}

func TestParsePongWeak(t *testing.T) {
	b := []byte{raknetUnconnectedPong}
	b = binary.BigEndian.AppendUint64(b, 1)
	b = binary.BigEndian.AppendUint64(b, 2)
	b = append(b, raknetMagic...)
	for name, pong := range map[string][]byte{
		"ends after the magic": b,
		"empty payload":        binary.BigEndian.AppendUint16(append([]byte{}, b...), 0),
	} {
		info, err := parsePong(pong)
		if err != nil || !info.weak {
			t.Errorf("%s: %+v %v, want a weak answer", name, info, err)
		}
	}
	if _, err := parsePong(b[:32]); err == nil {
		t.Errorf("a pong cut inside the magic was accepted")
	}
}
