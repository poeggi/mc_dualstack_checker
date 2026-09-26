// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"io"
	"maps"
	"net"
	"os"
	"slices"
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
	// MOTDRaw and MapRaw keep the colour codes; they are left out when
	// the text has none.
	MOTDRaw string `json:"motd_raw,omitempty"`
	MapRaw  string `json:"map_raw,omitempty"`
	Icon    string `json:"icon,omitempty"`
	Port4   int    `json:"port4,omitempty"`
	Port6   int    `json:"port6,omitempty"`
}

// SRVTarget is where a Java SRV record sends clients.
type SRVTarget struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

// PingResult is the outcome of one probe against one address and port.
//
// State is one of:
//
//	online      - the server answered
//	offline     - no valid answer: silence, a refusal by the host, or data
//	              that is not a Minecraft status
//	unreachable - a router or firewall on the way rejected the probe
//	no_route    - this host has no connectivity for the address family
//	no_dns      - the host name has no record in the requested family
//	dns_error   - resolving the host name failed
type PingResult struct {
	State  string      `json:"state"`
	IP     string      `json:"ip,omitempty"`
	RTTms  int64       `json:"rtt_ms,omitempty"`
	Error  string      `json:"error,omitempty"`
	SRV    *SRVTarget  `json:"srv,omitempty"`
	Info   *ServerInfo `json:"info,omitempty"`
	Cached bool        `json:"cached,omitempty"`
	AgeS   int         `json:"age_s"`
}

// edition is one Minecraft edition: how its servers are probed, and which
// SRV record its clients follow.
type edition struct {
	// network is "udp" or "tcp"; each probe adds the address family.
	network string
	// probe sends one status request to ip and port over network, "udp4"
	// for instance. host is the name a client would send, "" for a literal
	// address; editions that do not send one ignore it.
	probe func(ctx context.Context, network, ip string, port int, host string) (*ServerInfo, error)
	// At srvPort, clients follow the SRV record _<srvService>._<network>
	// of a name. srvPort is 0 when they follow none.
	srvService string
	srvPort    int
}

// editions are the editions the API probes, by their name in requests.
var editions = map[string]edition{
	"bedrock": {network: "udp", probe: pingBedrock},
	"java":    {network: "tcp", probe: pingJava, srvService: "minecraft", srvPort: 25565},
}

const defaultEdition = "bedrock"

// editionError answers a request for an unknown edition.
var editionError = func() string {
	names := slices.Sorted(maps.Keys(editions))
	last := len(names) - 1
	if last == 0 {
		return "edition must be " + names[0]
	}
	return "edition must be " + strings.Join(names[:last], ", ") + " or " + names[last]
}()

// ping runs exactly one probe. The address family is taken from ip.
func ping(ctx context.Context, ed edition, ip net.IP, port int, host string) PingResult {
	network := ed.network + "6"
	if ip.To4() != nil {
		network = ed.network + "4"
	}
	start := time.Now()
	info, err := ed.probe(ctx, network, ip.String(), port, host)
	if err == nil {
		info.clip()
		return PingResult{State: "online", RTTms: time.Since(start).Milliseconds(), Info: info}
	}
	return failure(err, ip)
}

// Longest server text kept. Status pages show a two-line MOTD; the other
// fields are short names and numbers. Colour codes can take more room than
// the text they colour. The limits keep cached results small.
const (
	motdMax  = 256
	rawMax   = 2048
	fieldMax = 64
)

func (i *ServerInfo) clip() {
	i.MOTD = clip(i.MOTD, motdMax)
	i.MOTDRaw = clip(i.MOTDRaw, rawMax)
	i.MapRaw = clip(i.MapRaw, rawMax)
	for _, f := range []*string{&i.Edition, &i.Version, &i.Protocol, &i.Gamemode, &i.Map, &i.ServerID} {
		*f = clip(*f, fieldMax)
	}
}

// clip cuts s to at most n bytes without splitting a character.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}

// failure is the result of a probe of ip that failed with err.
func failure(err error, ip net.IP) PingResult {
	if localNoRoute(err, ip) {
		return PingResult{State: "no_route", Error: noRouteReason(err)}
	}
	if r := rejection(err); r != "" {
		return PingResult{State: "unreachable", Error: "rejected (" + r + ")"}
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

// noRouteReason names why this host cannot use the family, for an error
// localNoRoute accepted.
func noRouteReason(err error) string {
	switch {
	case errors.Is(err, syscall.EADDRNOTAVAIL):
		return "no source address"
	case errors.Is(err, syscall.EAFNOSUPPORT):
		return "family not supported"
	}
	return "network unreachable"
}

// rejection names how something other than the probed host refused the
// probe, a router or firewall answering with ICMP unreachable or
// administratively prohibited. It is "" for other errors.
func rejection(err error) string {
	switch {
	case errors.Is(err, syscall.EHOSTUNREACH):
		return "no route to host"
	case errors.Is(err, syscall.EHOSTDOWN), noNetwork(err):
		return "host unknown"
	case errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
		return "prohibited"
	case errors.Is(err, syscall.ENETUNREACH):
		return "network unreachable"
	}
	return ""
}

// describe turns the error of a probe that got no valid answer into
// "<outcome> (<detail>)", independent of how the backend is implemented.
func describe(err error) string {
	var pe probeError
	var ne net.Error
	switch {
	case errors.As(err, &pe):
		return "invalid data (" + string(pe) + ")"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled),
		errors.Is(err, os.ErrDeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return "no response (timeout)"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "refused (port closed)"
	case errors.Is(err, syscall.ECONNRESET):
		return "refused (reset)"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "refused (closed)"
	}
	return "failed (unknown error)"
}

// formatted returns s when it carries colour codes, else "".
func formatted(s string) string {
	if strings.ContainsRune(s, 0xA7) {
		return s
	}
	return ""
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
