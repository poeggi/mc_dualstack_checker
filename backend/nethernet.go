// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"strings"
	"time"
)

const (
	// Largest /v1/join body read. The status has seven short fields.
	netherNetMaxBody = 16 << 10
	// netherNetAgent is the User-Agent the game client sends.
	netherNetAgent = "libhttpclient/1.0.0.0"
)

var nethernet = transport{name: "nethernet", network: "tcp", probe: pingNetherNet}

// Bedrock game modes by their number in the NetherNet status.
var netherNetModes = map[int]string{0: "Survival", 1: "Creative", 2: "Adventure"}

// pingNetherNet asks for the NetherNet status: GET /v1/join on the server
// port, over network ("tcp4" or "tcp6"). Vanilla servers have no TLS and
// answer plain HTTP. Servers that require HTTPS get the request once more
// over TLS when the plain one connected but brought no 2xx.
//
// A 2xx with an empty body is a weak answer: vanilla sends one when its
// LAN visibility is off. The round trip is the time from the request to
// the first byte of the answer.
func pingNetherNet(ctx context.Context, network string, t target) (*ServerInfo, time.Duration, error) {
	info, rtt, a, err := netherNetJoin(ctx, network, t, false)
	if err == nil || !a.connected {
		return info, rtt, err
	}
	info, rtt, b, terr := netherNetJoin(ctx, network, t, true)
	if terr == nil {
		return info, rtt, nil
	}
	// The TLS error tells more once the server spoke TLS.
	if b.handshake {
		return nil, 0, terr
	}
	return nil, 0, err
}

// attempt is how far one request got.
type attempt struct{ connected, handshake bool }

// netherNetJoin sends one GET /v1/join to t, over TLS when secure. The
// certificate is not checked: the status is public, and vanilla servers
// have none.
func netherNetJoin(ctx context.Context, network string, t target, secure bool) (*ServerInfo, time.Duration, attempt, error) {
	var a attempt
	ctx, cancel := context.WithTimeout(ctx, javaIOTimeout)
	defer cancel()
	addr := net.JoinHostPort(t.ip.String(), strconv.Itoa(t.port))
	host := t.host
	if host == "" {
		host = t.ip.String()
	}
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			},
			TLSClientConfig:   &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"http/1.1"}},
			DisableKeepAlives: true,
			// A status needs no compression and few headers; a server
			// gets no more room than the status takes.
			DisableCompression:     true,
			MaxResponseHeaderBytes: netherNetMaxBody,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	proto := "http"
	if secure {
		proto = "https"
	}
	var sent, first time.Time
	trace := &httptrace.ClientTrace{
		ConnectDone:          func(_, _ string, err error) { a.connected = err == nil },
		TLSHandshakeDone:     func(_ tls.ConnectionState, err error) { a.handshake = err == nil },
		WroteRequest:         func(httptrace.WroteRequestInfo) { sent = time.Now() },
		GotFirstResponseByte: func() { first = time.Now() },
	}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodGet,
		proto+"://"+net.JoinHostPort(host, strconv.Itoa(t.port))+"/v1/join", nil)
	if err != nil {
		return nil, 0, a, err
	}
	req.Header.Set("User-Agent", netherNetAgent)
	res, err := client.Do(req)
	if err != nil {
		// Past the connect, anything but a network error, a timeout or a
		// close is a reply that is no HTTP, such as a TLS alert.
		var op *net.OpError
		if a.connected && !errors.As(err, &op) && !errors.Is(err, context.DeadlineExceeded) &&
			!errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			err = probeError("no HTTP answer")
		}
		return nil, 0, a, err
	}
	defer res.Body.Close()
	var rtt time.Duration
	if !sent.IsZero() && first.After(sent) {
		rtt = first.Sub(sent)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, 0, a, probeError(fmt.Sprintf("HTTP %d", res.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, netherNetMaxBody+1))
	if err != nil {
		return nil, 0, a, err
	}
	if len(body) > netherNetMaxBody {
		return nil, 0, a, probeError("status too large")
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return &ServerInfo{weak: true}, rtt, a, nil
	}
	info, err := parseNetherNetStatus(body)
	return info, rtt, a, err
}

// parseNetherNetStatus decodes the /v1/join JSON. Each field is read on its
// own: one of an unexpected type is left out.
func parseNetherNetStatus(raw []byte) (*ServerInfo, error) {
	var st struct {
		Name, Protocol, Version, Level, Players, MaxPlayers, GameType json.RawMessage
	}
	if json.Unmarshal(raw, &st) != nil {
		return nil, probeError("not a NetherNet status")
	}
	name, level := jsonString(st.Name), jsonString(st.Level)
	info := &ServerInfo{
		ServerName:    name.plain(motdMax),
		ServerNameRaw: name.coded(rawMax),
		Version:       jsonString(st.Version).clean(fieldMax),
		Protocol:      jsonText(st.Protocol).clean(fieldMax),
		Level:         level.plain(fieldMax),
		LevelRaw:      level.coded(rawMax),
	}
	if n, ok := jsonInt(st.Players); ok {
		info.PlayersOnline = count(n)
	}
	if n, ok := jsonInt(st.MaxPlayers); ok {
		info.PlayersMax = count(n)
	}
	info.GameMode = jsonText(st.GameType).clean(fieldMax)
	if n, ok := jsonInt(st.GameType); ok && netherNetModes[n] != "" {
		info.GameMode = netherNetModes[n]
	}
	if info.ServerName == "" && info.Version == "" {
		return nil, probeError("not a NetherNet status")
	}
	return info, nil
}

// jsonText is raw as text: a string as it is, a whole number in decimal.
func jsonText(raw json.RawMessage) tainted {
	if n, ok := jsonInt(raw); ok {
		return tainted(strconv.Itoa(n))
	}
	return jsonString(raw)
}
