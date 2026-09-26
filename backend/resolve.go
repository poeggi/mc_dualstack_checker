// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"time"
)

// hostName returns host as a lowercase DNS name without a trailing dot, or
// "" unless it consists of letters, digits, hyphens and underscores in
// labels of at most 63 characters, the characters resolvers accept.
func hostName(host string) string {
	name := strings.ToLower(strings.TrimSuffix(host, "."))
	if name == "" || len(name) > maxHostLen {
		return ""
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 {
			return ""
		}
		for _, c := range label {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
				return ""
			}
		}
	}
	return name
}

// localDomains only resolve inside private networks: reserved and
// customary private names, plus the checker host's own search domains.
var localDomains = append([]string{
	"localhost", "local", "internal", "lan", "home", "corp", "localdomain",
	"intranet", "private", "arpa", "test", "example", "invalid",
}, searchDomains()...)

// searchDomains reads the search and domain lines of /etc/resolv.conf.
func searchDomains() []string {
	b, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) > 1 && (f[0] == "search" || f[0] == "domain") {
			for _, d := range f[1:] {
				if d = strings.ToLower(strings.Trim(d, ".")); d != "" {
					out = append(out, d)
				}
			}
		}
	}
	return out
}

// localName reports whether name can only be internal: a single label, or
// a name in localDomains. Such names are never looked up.
func localName(name string) bool {
	if !strings.Contains(name, ".") {
		return true
	}
	for _, d := range localDomains {
		if name == d || strings.HasSuffix(name, "."+d) {
			return true
		}
	}
	return false
}

// resolveFamily returns the first public address of name in family ("4" or
// "6"), nil when there is none. The name is looked up as absolute, so the
// host's search domains are never appended. Local names, and names that
// point only to internal addresses, come back nil like missing records,
// so answers reveal nothing about internal names.
func resolveFamily(ctx context.Context, name, family string) (net.IP, error) {
	if localName(name) {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip"+family, name+".")
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if !filterInternal || !internalTarget(ip) {
			return ip, nil
		}
	}
	return nil, nil
}

// srvResolver looks up SRV records; tests replace it.
var srvResolver = net.DefaultResolver.LookupSRV

// lookupSRV returns where the SRV record _<service>._<proto> of name sends
// clients, nil when there is none. Clients use the name as given when the
// lookup fails, and so does this.
//
// The resolver stops at srvTimeout. Some system resolvers, the one on
// Windows among them, cannot be stopped: after srvWait the lookup is left
// to finish on its own, as the net package does with Windows address
// lookups. srvWait is later than srvTimeout, so it never cuts short a
// resolver that keeps the limit.
func lookupSRV(ctx context.Context, service, proto, name string) *SRVTarget {
	if localName(name) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, srvTimeout)
	defer cancel()
	found := make(chan []*net.SRV, 1)
	go func() {
		_, addrs, _ := srvResolver(ctx, service, proto, name+".")
		found <- addrs
	}()
	wait := time.NewTimer(srvWait)
	defer wait.Stop()
	select {
	case addrs := <-found:
		return srvTarget(addrs)
	case <-wait.C:
		return nil
	}
}

// srvTarget is the first usable record, in the resolver's order of
// priority and weight. A target of "." offers no service.
func srvTarget(addrs []*net.SRV) *SRVTarget {
	for _, a := range addrs {
		if host := hostName(a.Target); host != "" && a.Port != 0 {
			return &SRVTarget{Host: host, Port: int(a.Port)}
		}
	}
	return nil
}

func lookupReason(err error) string {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsTimeout {
		return "timeout"
	}
	return "error"
}
