// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"io"
	"log"
	"maps"
	"net"
	"os"
	"slices"
	"strings"
	"syscall"
	"time"
)

// ServerInfo is what a successful ping yields, independent of edition. The
// player counts are left out when the server sends none.
type ServerInfo struct {
	// Transport is how the server answered: "nethernet", "raknet" or "tcp".
	Transport string `json:"transport"`
	Edition   string `json:"edition,omitempty"`
	// MOTD is a Java server's message of the day, ServerName a Bedrock
	// server's name: Mojang's words for the text a server shows.
	MOTD          string `json:"motd,omitempty"`
	ServerName    string `json:"server_name,omitempty"`
	Version       string `json:"version,omitempty"`
	Protocol      string `json:"protocol,omitempty"`
	PlayersOnline *int   `json:"players_online,omitempty"`
	PlayersMax    *int   `json:"players_max,omitempty"`
	GameMode      string `json:"game_mode,omitempty"`
	Level         string `json:"level,omitempty"`
	ServerID      string `json:"server_id,omitempty"`
	// The raw forms keep the colour codes; they are left out when the text
	// has none.
	MOTDRaw       string `json:"motd_raw,omitempty"`
	ServerNameRaw string `json:"server_name_raw,omitempty"`
	LevelRaw      string `json:"level_raw,omitempty"`
	Icon          string `json:"icon,omitempty"`
	// Contact is how to reach the operators, as a Java server sends it.
	Contact string `json:"contact,omitempty"`
	Port4   int    `json:"port4,omitempty"`
	Port6   int    `json:"port6,omitempty"`
	// weak marks an answer without a status: the server is there but
	// tells nothing about itself.
	weak bool
}

// count is a player count as ServerInfo holds it.
func count(n int) *int { return &n }

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
	State string `json:"state"`
	IP    string `json:"ip,omitempty"`
	RTTms int64  `json:"rtt_ms,omitempty"`
	Error string `json:"error,omitempty"`
	// Errors has the error of each transport tried, by transport, when none
	// answered.
	Errors map[string]string `json:"errors,omitempty"`
	SRV    *SRVTarget        `json:"srv,omitempty"`
	Info   *ServerInfo       `json:"info,omitempty"`
	Cached bool              `json:"cached,omitempty"`
	AgeS   int               `json:"age_s"`
}

// target is one address and port to probe, and what a client would send
// along. Editions that send no name or ID ignore them.
type target struct {
	ip   net.IP
	port int
	// host is the name a client would send, "" for a literal address.
	host string
	// id is the connection ID a client would send, "" for none.
	id string
}

// transport is one way to ask a server for its status.
type transport struct {
	// name is the transport in answers: info.transport and the keys of errors.
	name string
	// network is "udp" or "tcp"; each probe adds the address family.
	network string
	// probe sends one status request to t over network, "udp4" for
	// instance. It returns the round trip as the transport's client measures
	// it. It ends soon after ctx is cancelled.
	probe func(ctx context.Context, network string, t target) (*ServerInfo, time.Duration, error)
}

var (
	raknet  = transport{name: "raknet", network: "udp", probe: pingBedrock}
	javaTCP = transport{name: "tcp", network: "tcp", probe: pingJava}
)

// edition is one Minecraft edition: how its servers are probed, and which
// SRV record its clients follow.
type edition struct {
	// transports are raced in this order of preference, see ping. A failed
	// probe reports the error of the last one, the edition's classic
	// transport.
	transports []transport
	// At srvPort, clients follow the SRV record _<srvService>._tcp of a
	// name. srvPort is 0 when they follow none.
	srvService string
	srvPort    int
	// connectionIDs is whether clients can send a connection ID.
	connectionIDs bool
}

// editions are the editions the API probes, by their name in requests.
var editions = map[string]edition{
	"bedrock": {transports: []transport{nethernet, raknet}},
	"java":    {transports: []transport{javaTCP}, srvService: "minecraft", srvPort: 25565, connectionIDs: true},
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

// The race of an edition's transports, as in Happy Eyeballs (RFC 8305).
const (
	// headStart is how long a transport runs alone before the next starts.
	headStart = 250 * time.Millisecond
	// grace is how long a status from a later transport waits for the more
	// preferred transports that still run, and a weak answer for any status.
	grace = 500 * time.Millisecond
)

// outcome is how the probe of transport i ended.
type outcome struct {
	i    int
	info *ServerInfo
	rtt  time.Duration
	err  error
}

// answered reports whether o brought an answer, a status or a weak one.
func (o *outcome) answered() bool { return o != nil && o.err == nil }

// status reports whether o brought a status, more than a weak answer.
func (o *outcome) status() bool { return o.answered() && !o.info.weak }

// ping probes t with the edition's transports. The first starts at once, each
// next one headStart later, or at once when all started ones have ended
// without a status. The most preferred status wins; a status from a later
// transport waits at most grace for the ones before it. A weak answer counts
// when no transport brings a status within grace. The address family is taken
// from t.ip.
func ping(ctx context.Context, ed edition, t target) PingResult {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	family := "6"
	if t.ip.To4() != nil {
		family = "4"
	}
	n := len(ed.transports)
	outcomes := make(chan outcome, n)
	ended := make([]*outcome, n)
	started := 0
	start := func() {
		i, s := started, ed.transports[started]
		started++
		go func() {
			o := outcome{i: i}
			// A probe reads what a server sends; should that ever make it
			// panic, the probe fails instead of the whole backend.
			defer func() {
				if r := recover(); r != nil {
					log.Printf("probe %s: %v", s.name, r)
					o.info, o.err = nil, probeError("unreadable answer")
				}
				outcomes <- o
			}()
			o.info, o.rtt, o.err = s.probe(ctx, s.network+family, t)
		}()
	}
	anyStatus := func() bool { return slices.ContainsFunc(ended, (*outcome).status) }
	start()
	next := time.NewTimer(headStart)
	defer next.Stop()
	var graceUp <-chan time.Time
	for {
		// The most preferred transport that runs or brought a status decides:
		// its status wins, and while it runs the others wait for it.
		first := -1
		for i := 0; i < started && first < 0; i++ {
			if ended[i] == nil || ended[i].status() {
				first = i
			}
		}
		switch {
		case first >= 0 && ended[first] != nil:
			return ed.online(ended[first])
		case first >= 0:
			if graceUp == nil && slices.ContainsFunc(ended, (*outcome).answered) {
				graceUp = time.After(grace)
			}
		case started < n:
			start()
			next.Reset(headStart)
			continue
		default:
			return ed.settle(ended, t.ip)
		}
		select {
		case o := <-outcomes:
			ended[o.i] = &o
		case <-next.C:
			if started < n && !anyStatus() {
				start()
				next.Reset(headStart)
			}
		case <-graceUp:
			if i := slices.IndexFunc(ended, (*outcome).status); i >= 0 {
				return ed.online(ended[i])
			}
			return ed.online(ended[slices.IndexFunc(ended, (*outcome).answered)])
		}
	}
}

// online is the answer of transport o.i. A weak answer tells only the transport.
func (ed edition) online(o *outcome) PingResult {
	info := o.info
	if info.weak {
		info = &ServerInfo{}
	}
	info.Transport = ed.transports[o.i].name
	return PingResult{State: "online", RTTms: o.rtt.Milliseconds(), Info: info}
}

// settle is the result when every transport ended without a status: the most
// preferred weak answer, else the failure of the classic transport, with the
// error of each transport.
func (ed edition) settle(ended []*outcome, ip net.IP) PingResult {
	if i := slices.IndexFunc(ended, (*outcome).answered); i >= 0 {
		return ed.online(ended[i])
	}
	res := failure(ended[len(ended)-1].err, ip)
	res.Errors = make(map[string]string, len(ended))
	for _, o := range ended {
		res.Errors[ed.transports[o.i].name] = failure(o.err, ip).Error
	}
	return res
}

// Longest server text kept. Status pages show a two-line MOTD; the other
// fields are short names and numbers. Colour codes can take more room than
// the text they colour. The limits keep cached results small.
const (
	motdMax  = 256
	rawMax   = 2048
	fieldMax = 64
)

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

// noStatus is a server that accepted the connection and closed it without
// a status. The text names the likely reasons.
type noStatus string

func (e noStatus) Error() string { return "connected, no status (" + string(e) + ")" }

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
	var ns noStatus
	var ne net.Error
	switch {
	case errors.As(err, &pe):
		return "invalid data (" + string(pe) + ")"
	case errors.As(err, &ns):
		return ns.Error()
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
