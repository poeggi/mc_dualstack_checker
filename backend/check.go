package main

import (
	"context"
	"errors"
	"fmt"
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

// FamilyResult is the outcome for one IP family.
//
// State is one of:
//
//	online   - server answered on Port
//	offline  - no answer on any of PortsTried
//	no_dns   - the hostname has no record for this family
//	omitted  - input was a literal address of the other family
//	no_route - the checker host itself has no connectivity for this family
type FamilyResult struct {
	State      string      `json:"state"`
	IP         string      `json:"ip,omitempty"`
	Port       int         `json:"port,omitempty"`
	PortsTried []int       `json:"ports_tried,omitempty"`
	Reason     string      `json:"reason,omitempty"`
	Info       *ServerInfo `json:"info,omitempty"`
}

type CheckResponse struct {
	Host      string       `json:"host"`
	Edition   string       `json:"edition"`
	QueriedAt int64        `json:"queried_at"`
	IPv4      FamilyResult `json:"ipv4"`
	IPv6      FamilyResult `json:"ipv6"`
	Log       []string     `json:"log"`
}

type CheckRequest struct {
	Host       string
	Edition    string
	Port4      int // 0 = default for edition
	Port6      int // 0 = same as Port4
	NoFallback bool
}

var defaultPorts = map[string][2]int{
	"bedrock": {19132, 19133},
	"java":    {25565, 25565},
}

type checker struct {
	log []string
}

func (c *checker) logf(format string, a ...any) {
	c.log = append(c.log, fmt.Sprintf(format, a...))
}

func runCheck(ctx context.Context, req CheckRequest) CheckResponse {
	c := &checker{}
	defaults := defaultPorts[req.Edition]
	port4 := req.Port4
	if port4 == 0 {
		port4 = defaults[0]
	}
	port6 := req.Port6
	if port6 == 0 {
		port6 = port4
	}

	resp := CheckResponse{Host: req.Host, Edition: req.Edition, QueriedAt: time.Now().Unix()}

	var ip4, ip6 string
	literal := net.ParseIP(strings.Trim(req.Host, "[]"))
	switch {
	case literal != nil && literal.To4() != nil:
		c.logf("Input is a literal IPv4 address, skipping DNS")
		ip4 = literal.String()
		resp.IPv6 = FamilyResult{State: "omitted", Reason: "Input is a literal IPv4 address"}
	case literal != nil:
		c.logf("Input is a literal IPv6 address, skipping DNS")
		ip6 = literal.String()
		resp.IPv4 = FamilyResult{State: "omitted", Reason: "Input is a literal IPv6 address"}
	default:
		ip4, ip6 = resolve(ctx, req.Host, c)
		if ip4 == "" {
			resp.IPv4 = FamilyResult{State: "no_dns", Reason: "No A record found"}
		}
		if ip6 == "" {
			resp.IPv6 = FamilyResult{State: "no_dns", Reason: "No AAAA record found"}
		}
	}

	// Fallback order mirrors the edition defaults: an IPv6 Bedrock listener
	// commonly sits on 19133 while the form default is the IPv4 port.
	fb4 := []int{defaults[0]}
	fb6 := []int{defaults[1]}
	if req.Edition == "bedrock" {
		fb6 = append(fb6, 19133, 19132)
	}
	if req.NoFallback {
		fb4, fb6 = nil, nil
	}

	type job struct {
		family string
		ip     string
		port   int
		fb     []int
		dst    *FamilyResult
	}
	jobs := []job{}
	if ip4 != "" {
		jobs = append(jobs, job{"IPv4", ip4, port4, fb4, &resp.IPv4})
	}
	if ip6 != "" {
		jobs = append(jobs, job{"IPv6", ip6, port6, fb6, &resp.IPv6})
	}
	for _, j := range jobs {
		*j.dst = checkFamily(ctx, c, req, j.family, j.ip, j.port, j.fb)
	}
	resp.Log = c.log
	return resp
}

func resolve(ctx context.Context, host string, c *checker) (ip4, ip6 string) {
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if addrs, err := net.DefaultResolver.LookupIP(rctx, "ip4", host); err == nil && len(addrs) > 0 {
		ip4 = addrs[0].String()
		c.logf("Resolved IPv4: %s", ip4)
	} else {
		c.logf("Resolved IPv4: no A record")
	}
	if addrs, err := net.DefaultResolver.LookupIP(rctx, "ip6", host); err == nil && len(addrs) > 0 {
		ip6 = addrs[0].String()
		c.logf("Resolved IPv6: %s", ip6)
	} else {
		c.logf("Resolved IPv6: no AAAA record")
	}
	return
}

func checkFamily(ctx context.Context, c *checker, req CheckRequest, family, ip string, port int, fallbacks []int) FamilyResult {
	res := FamilyResult{State: "offline", IP: ip}
	ports := []int{port}
	for _, p := range fallbacks {
		if p != port && !contains(ports, p) {
			ports = append(ports, p)
		}
	}
	network := map[string]string{"IPv4": "4", "IPv6": "6"}[family]
	if req.Edition == "bedrock" {
		network = "udp" + network
	} else {
		network = "tcp" + network
	}

	for i, p := range ports {
		if i > 0 {
			c.logf("Port %d failed, retrying port %d", ports[i-1], p)
		}
		c.logf("Checking %s: %s port %d", family, ip, p)
		res.PortsTried = append(res.PortsTried, p)

		var info *ServerInfo
		var err error
		if req.Edition == "bedrock" {
			info, err = pingBedrock(ctx, network, ip, p)
		} else {
			info, err = pingJava(ctx, network, ip, p, req.Host)
		}
		if err == nil {
			c.logf("ONLINE: %s responded on port %d", family, p)
			res.State, res.Port, res.Info = "online", p, info
			return res
		}
		if isNoRoute(err) {
			c.logf("ERROR: checker has no %s connectivity: %v", family, err)
			res.State = "no_route"
			res.Reason = "The checker host has no " + family + " connectivity"
			return res
		}
		c.logf("%s port %d: %v", family, p, err)
	}
	c.logf("OFFLINE: %s did not respond on any port", family)
	return res
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

func contains(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
