// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// Protection for the endpoints: a 60 s result cache that coalesces
// identical probes, a per-client sliding window of distinct systems, a
// per-client cooldown after too many health requests, a per-client request
// budget, a global cap on in-flight probes and name lookups, and a filter
// that keeps probes off internal networks.

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"time"
)

const (
	cacheTTL      = 60 * time.Second
	cacheMax      = 10000
	systemsWindow = 60 * time.Second
	systemsMax    = 8
	cooldown      = 60 * time.Second
	rlBurst       = 16
	rlBatch       = 4 // tokens that arrive together
	rlBatchEvery  = 7 * time.Second
	clientIdle    = 2 * time.Minute
	clientsMax    = 10000
	healthMax     = 4
	maxInFlight   = 128
)

// -- result cache ------------------------------------------------

type cacheEntry struct {
	res   PingResult
	at    time.Time
	ready chan struct{}
}

type probeCache struct {
	mu sync.Mutex
	m  map[string]*cacheEntry
}

var cache = &probeCache{m: map[string]*cacheEntry{}}

// get returns the cached result for key, or runs probe once for all
// concurrent callers. A probe that returns ok=false is not stored. With
// cacheMax entries, new probes run uncached until sweep frees room.
func (c *probeCache) get(ctx context.Context, key string, probe func() (PingResult, bool)) PingResult {
	c.mu.Lock()
	if e := c.m[key]; e != nil {
		select {
		case <-e.ready:
			if age := time.Since(e.at); age < cacheTTL {
				c.mu.Unlock()
				res := e.res
				res.Cached, res.AgeS = true, int(age.Seconds())
				return res
			}
		default:
			c.mu.Unlock()
			select {
			case <-e.ready:
				return e.res
			case <-ctx.Done():
				return PingResult{State: "offline", Error: describe(ctx.Err())}
			}
		}
	}
	if len(c.m) >= cacheMax {
		c.mu.Unlock()
		res, _ := probe()
		return res
	}
	e := &cacheEntry{ready: make(chan struct{})}
	c.m[key] = e
	c.mu.Unlock()

	res, ok := probe()
	c.mu.Lock()
	e.res, e.at = res, time.Now()
	if !ok {
		delete(c.m, key)
	}
	close(e.ready)
	c.mu.Unlock()
	return res
}

// sweep drops expired results.
func (c *probeCache) sweep() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.m {
		select {
		case <-e.ready:
			if time.Since(e.at) >= cacheTTL {
				delete(c.m, k)
			}
		default:
		}
	}
}

// -- per-client limits ---------------------------------------------

type client struct {
	tokens  int
	refill  time.Time // when the next batch of tokens arrives
	last    time.Time // last request, for sweep
	systems map[string]time.Time
	health  []time.Time
	blocked time.Time
	cause   string
}

type limiter struct {
	mu      sync.Mutex
	clients map[string]*client
}

var limits = &limiter{clients: map[string]*client{}}

// overflowClient is charged for new clients while the table holds
// clientsMax, so they share one budget until sweep frees room.
const overflowClient = "overflow"

// admitProbe charges one probe request to ip and records the target system.
// A system new to a full window blocks the client until the oldest system
// leaves the window, but at least rlBatchEvery.
func (l *limiter) admitProbe(ip, system string) (wait int, reason string) {
	return l.admit(ip, func(c *client, now time.Time) (string, time.Duration) {
		oldest := now
		for s, t := range c.systems {
			if now.Sub(t) > systemsWindow {
				delete(c.systems, s)
			} else if t.Before(oldest) {
				oldest = t
			}
		}
		if _, seen := c.systems[system]; !seen && len(c.systems) >= systemsMax {
			return "systems", max(oldest.Add(systemsWindow).Sub(now), rlBatchEvery)
		}
		c.systems[system] = now
		return "", 0
	})
}

// admitHealth charges one health request to ip. More than healthMax
// within healthInterval start the cooldown.
func (l *limiter) admitHealth(ip string) (wait int, reason string) {
	return l.admit(ip, func(c *client, now time.Time) (string, time.Duration) {
		recent := c.health[:0]
		for _, t := range c.health {
			if now.Sub(t) < healthInterval {
				recent = append(recent, t)
			}
		}
		c.health = append(recent, now)
		if len(c.health) > healthMax {
			return "health", cooldown
		}
		return "", 0
	})
}

// admit applies a running block and the request budget, then check, which
// names the limit a request breaks and how long that blocks the client, or
// returns "". It returns the seconds to wait and the limit hit when the
// client is over one.
func (l *limiter) admit(ip string, check func(c *client, now time.Time) (string, time.Duration)) (wait int, reason string) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	c := l.clients[ip]
	if c == nil && len(l.clients) >= clientsMax {
		ip = overflowClient
		c = l.clients[ip]
	}
	if c == nil {
		c = &client{tokens: rlBurst, refill: now.Add(rlBatchEvery), systems: map[string]time.Time{}}
		l.clients[ip] = c
	}
	c.last = now
	if now.Before(c.blocked) {
		return secondsUntil(c.blocked, now), c.cause
	}
	// Every batch due since the last one is credited. A full bucket
	// restarts the clock.
	if !now.Before(c.refill) {
		batches := int(now.Sub(c.refill)/rlBatchEvery) + 1
		c.tokens += batches * rlBatch
		c.refill = c.refill.Add(time.Duration(batches) * rlBatchEvery)
		if c.tokens >= rlBurst {
			c.tokens = rlBurst
			c.refill = now.Add(rlBatchEvery)
		}
	}
	if c.tokens < 1 {
		return secondsUntil(c.refill, now), "rate"
	}
	c.tokens--

	if cause, block := check(c, now); cause != "" {
		c.blocked, c.cause = now.Add(block), cause
		return secondsUntil(c.blocked, now), cause
	}
	return 0, ""
}

// sweep drops clients idle for clientIdle and out of cooldown. They would
// be treated exactly like new ones.
func (l *limiter) sweep() {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, c := range l.clients {
		if now.Sub(c.last) > clientIdle && now.After(c.blocked) {
			delete(l.clients, k)
		}
	}
}

func secondsUntil(t, now time.Time) int {
	s := int(t.Sub(now).Seconds()) + 1
	if s < 1 {
		s = 1
	}
	return s
}

// -- global in-flight cap ------------------------------------------

var inFlight = make(chan struct{}, maxInFlight)

func acquireSlot() bool {
	select {
	case inFlight <- struct{}{}:
		return true
	default:
		return false
	}
}

func releaseSlot() { <-inFlight }

// -- target filter -------------------------------------------------

// internalNets are the ranges a probe must not reach while filterInternal
// is set: the checker host itself, its local networks, and addresses that
// are not unicast on the internet.
var internalNets = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
		"169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.168.0.0/16",
		"198.18.0.0/15", "224.0.0.0/3",
		"::/127", "64:ff9b:1::/48", "100::/64", "fc00::/7", "fe80::/10",
		"fec0::/10", "ff00::/8",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

var nat64 = netip.MustParsePrefix("64:ff9b::/96")

// internalTarget reports whether ip lies in internalNets, directly or as
// the IPv4 address inside an IPv4-mapped or NAT64 address.
func internalTarget(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	a = a.Unmap()
	if nat64.Contains(a) {
		b := a.As16()
		a = netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})
	}
	for _, p := range internalNets {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
