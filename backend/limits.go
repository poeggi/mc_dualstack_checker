// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// Protection for the probe endpoint: a 60 s result cache that coalesces
// identical probes, a per-client cooldown after too many distinct systems,
// a per-client request budget, and a global cap on in-flight probes.

import (
	"context"
	"sync"
	"time"
)

const (
	cacheTTL      = 60 * time.Second
	cacheMax      = 10000
	systemsWindow = 60 * time.Second
	systemsMax    = 10
	cooldown      = 60 * time.Second
	rlBurst       = 20
	rlRefill      = time.Second
	clientIdle    = 10 * time.Minute
	maxInFlight   = 64
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
// concurrent callers. A probe that returns ok=false is not stored.
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
				return PingResult{State: "offline", Error: ctx.Err().Error()}
			}
		}
	}
	e := &cacheEntry{ready: make(chan struct{})}
	c.m[key] = e
	if len(c.m) > cacheMax {
		c.sweepLocked()
	}
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

func (c *probeCache) sweepLocked() {
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
	tokens  float64
	last    time.Time
	systems map[string]time.Time
	blocked time.Time
}

type limiter struct {
	mu      sync.Mutex
	clients map[string]*client
	sweep   time.Time
}

var limits = &limiter{clients: map[string]*client{}, sweep: time.Now()}

// admit charges one request to ip and, for probes, records the target
// system. It returns the seconds to wait when the client is over a limit.
func (l *limiter) admit(ip, system string) (wait int, reason string) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.sweep) > clientIdle {
		for k, c := range l.clients {
			if now.Sub(c.last) > clientIdle && now.After(c.blocked) {
				delete(l.clients, k)
			}
		}
		l.sweep = now
	}
	c := l.clients[ip]
	if c == nil {
		c = &client{tokens: rlBurst, last: now, systems: map[string]time.Time{}}
		l.clients[ip] = c
	}
	if now.Before(c.blocked) {
		return secondsUntil(c.blocked, now), "cooldown"
	}
	c.tokens += now.Sub(c.last).Seconds() / rlRefill.Seconds()
	if c.tokens > rlBurst {
		c.tokens = rlBurst
	}
	c.last = now
	if c.tokens < 1 {
		return secondsUntil(now.Add(time.Duration((1-c.tokens)*float64(rlRefill))), now), "rate"
	}
	c.tokens--

	if system == "" {
		return 0, ""
	}
	for s, t := range c.systems {
		if now.Sub(t) > systemsWindow {
			delete(c.systems, s)
		}
	}
	if _, seen := c.systems[system]; !seen && len(c.systems) >= systemsMax {
		c.blocked = now.Add(cooldown)
		return secondsUntil(c.blocked, now), "cooldown"
	}
	c.systems[system] = now
	return 0, ""
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

func acquireProbe() bool {
	select {
	case inFlight <- struct{}{}:
		return true
	default:
		return false
	}
}

func releaseProbe() { <-inFlight }
