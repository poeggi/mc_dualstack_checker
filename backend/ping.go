// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
	"time"
)

// ServerInfo is what a successful ping yields, independent of edition.
type ServerInfo struct {
	Edition       string `json:"edition,omitempty"`
	MOTD          string `json:"motd,omitempty"`
	Version       string `json:"version,omitempty"`
	Protocol      string `json:"protocol,omitempty"`
	PlayersOnline int    `json:"players_online"`
	PlayersMax    int    `json:"players_max"`
	Gamemode      string `json:"gamemode,omitempty"`
	Map           string `json:"map,omitempty"`
	ServerID      string `json:"server_id,omitempty"`
}

// PingResult is the outcome of one probe against one address and port.
//
// State is one of:
//
//	online      - the server answered
//	offline     - no answer, or the server itself refused the port
//	unreachable - a router or firewall on the way rejected the probe
//	no_route    - this host has no connectivity for the address family
//	no_dns      - the host name has no record in the requested family
//	dns_error   - resolving the host name failed
type PingResult struct {
	State  string      `json:"state"`
	IP     string      `json:"ip,omitempty"`
	RTTms  int64       `json:"rtt_ms,omitempty"`
	Error  string      `json:"error,omitempty"`
	Info   *ServerInfo `json:"info,omitempty"`
	Cached bool        `json:"cached,omitempty"`
	AgeS   int         `json:"age_s"`
}

var editionNetworks = map[string]string{"bedrock": "udp", "java": "tcp"}

// ping runs exactly one probe. The address family is taken from ip.
func ping(ctx context.Context, edition string, ip net.IP, port int, hostLabel string) PingResult {
	network := editionNetworks[edition]
	if ip.To4() != nil {
		network += "4"
	} else {
		network += "6"
	}

	start := time.Now()
	var info *ServerInfo
	var err error
	if edition == "bedrock" {
		info, err = pingBedrock(ctx, network, ip.String(), port)
	} else {
		info, err = pingJava(ctx, network, ip.String(), port, hostLabel)
	}
	if err == nil {
		return PingResult{State: "online", RTTms: time.Since(start).Milliseconds(), Info: info}
	}
	return failure(err, ip)
}

// failure is the result of a probe of ip that failed with err.
func failure(err error, ip net.IP) PingResult {
	switch {
	case localNoRoute(err, ip):
		return PingResult{State: "no_route", Error: describe(err)}
	case rejected(err):
		msg := describe(err)
		if errors.Is(err, syscall.ENETUNREACH) {
			msg += " (rejected by a router, ICMP unreachable)"
		}
		return PingResult{State: "unreachable", Error: msg}
	}
	return PingResult{State: "offline", Error: describe(err)}
}

// probeError is a reply that violates the edition's protocol.
type probeError string

func (e probeError) Error() string { return string(e) }

// localNoRoute reports whether the error means this host cannot use the
// address family of ip at all. "Network unreachable" can also be a
// router's answer; a UDP socket connected to ip asks only the local routing
// table, without sending anything, and so tells the two apart.
func localNoRoute(err error, ip net.IP) bool {
	if errors.Is(err, syscall.EADDRNOTAVAIL) || errors.Is(err, syscall.EAFNOSUPPORT) {
		return true
	}
	if !errors.Is(err, syscall.ENETUNREACH) {
		return false
	}
	c, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: ip, Port: 9})
	if err != nil {
		return true
	}
	c.Close()
	return false
}

// rejected reports whether something other than the probed host refused
// the probe: a router or firewall answering with ICMP unreachable or
// administratively prohibited.
func rejected(err error) bool {
	for _, e := range []error{syscall.ENETUNREACH, syscall.EHOSTUNREACH, syscall.EHOSTDOWN,
		syscall.ENONET, syscall.EACCES, syscall.EPERM} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// describe turns a probe error into a short reason that does not depend on
// how the backend is implemented.
func describe(err error) string {
	var pe probeError
	var ne net.Error
	switch {
	case errors.As(err, &pe):
		return "invalid reply: " + string(pe)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled),
		errors.Is(err, os.ErrDeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return "timeout"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused (port closed)"
	case errors.Is(err, syscall.EHOSTUNREACH):
		return "no route to host (rejected by a router or firewall, ICMP unreachable)"
	case errors.Is(err, syscall.EHOSTDOWN), errors.Is(err, syscall.ENONET):
		return "host unknown (rejected by a router, ICMP unreachable)"
	case errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
		return "permission denied (rejected by a firewall, ICMP administratively prohibited)"
	case errors.Is(err, syscall.ECONNRESET):
		return "connection reset"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "connection closed"
	case errors.Is(err, syscall.ENETUNREACH):
		return "network unreachable"
	case errors.Is(err, syscall.EADDRNOTAVAIL):
		return "no source address for this family"
	case errors.Is(err, syscall.EAFNOSUPPORT):
		return "address family not supported"
	}
	return "probe failed"
}

// stripFormatting removes Minecraft "section sign" colour codes.
func stripFormatting(s string) string {
	var out strings.Builder
	skip := false
	for _, r := range s {
		if skip {
			skip = false
			continue
		}
		if r == 0xA7 {
			skip = true
			continue
		}
		out.WriteRune(r)
	}
	return strings.TrimSpace(out.String())
}
