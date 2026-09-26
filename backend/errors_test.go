// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"io"
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
		name, state, reason string
		err                 error
	}{
		{"port closed", "offline", "refused (port closed)", dial(syscall.ECONNREFUSED)},
		{"reset", "offline", "refused (reset)", dial(syscall.ECONNRESET)},
		{"closed", "offline", "refused (closed)", io.ErrUnexpectedEOF},
		{"timeout", "offline", "no response (timeout)", context.DeadlineExceeded},
		{"invalid reply", "offline", "invalid data (bad raknet magic)", probeError("bad raknet magic")},
		{"no status", "offline", "connected, no status (status disabled)", noStatus("status disabled")},
		{"other", "offline", "failed (unknown error)", errors.New("x")},
		{"host unreachable", "unreachable", "rejected (no route to host)", dial(syscall.EHOSTUNREACH)},
		{"administratively prohibited", "unreachable", "rejected (prohibited)", dial(syscall.EACCES)},
		{"network unreachable, local route exists", "unreachable", "rejected (network unreachable)", dial(syscall.ENETUNREACH)},
		{"no source address", "no_route", "no source address", dial(syscall.EADDRNOTAVAIL)},
	} {
		got := failure(c.err, loopback)
		if got.State != c.state || got.Error != c.reason {
			t.Errorf("%s: %q %q, want %q %q", c.name, got.State, got.Error, c.state, c.reason)
		}
	}
}
