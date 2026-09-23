// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"net"
	"os"
	"syscall"
	"testing"
)

func TestFailure(t *testing.T) {
	dial := func(errno syscall.Errno) error {
		return &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", errno)}
	}
	// The local routing table always has a route to loopback, so a
	// "network unreachable" for it must have come from elsewhere.
	loopback := net.ParseIP("127.0.0.1")
	for _, c := range []struct {
		name, state string
		err         error
	}{
		{"port closed", "offline", dial(syscall.ECONNREFUSED)},
		{"timeout", "offline", context.DeadlineExceeded},
		{"invalid reply", "offline", probeError("bad raknet magic")},
		{"host unreachable", "unreachable", dial(syscall.EHOSTUNREACH)},
		{"administratively prohibited", "unreachable", dial(syscall.EACCES)},
		{"network unreachable, local route exists", "unreachable", dial(syscall.ENETUNREACH)},
		{"no source address", "no_route", dial(syscall.EADDRNOTAVAIL)},
	} {
		if got := failure(c.err, loopback).State; got != c.state {
			t.Errorf("%s: state %q, want %q", c.name, got, c.state)
		}
	}
}
