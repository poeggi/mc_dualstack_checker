// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"net"
	"strings"
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
//	online   - the server answered
//	offline  - no answer
//	no_route - this host has no connectivity for the address family
type PingResult struct {
	State  string      `json:"state"`
	RTTms  int64       `json:"rtt_ms,omitempty"`
	Error  string      `json:"error,omitempty"`
	Info   *ServerInfo `json:"info,omitempty"`
	Cached bool        `json:"cached,omitempty"`
	AgeS   int         `json:"age_s,omitempty"`
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
	if isNoRoute(err) {
		return PingResult{State: "no_route", Error: err.Error()}
	}
	return PingResult{State: "offline", Error: err.Error()}
}

// isNoRoute reports whether the error means the local host cannot use this
// address family at all, as opposed to the remote server not answering.
func isNoRoute(err error) bool {
	var opErr *net.OpError
	if !errors.As(err, &opErr) {
		return false
	}
	msg := strings.ToLower(opErr.Err.Error())
	return strings.Contains(msg, "unreachable") ||
		strings.Contains(msg, "no route") ||
		strings.Contains(msg, "cannot assign requested address") ||
		strings.Contains(msg, "address family not supported")
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
