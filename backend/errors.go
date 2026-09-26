// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"syscall"
)

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
