// SPDX-License-Identifier: AGPL-3.0-or-later

// mc_dualstack_check backend. The one network primitive the browser cannot
// do itself: a Minecraft status probe. Everything else (fallback order,
// dual-stack logic, rendering) lives in the frontend. The API is documented
// in docs/api.md.
//
//	GET /ping?ip=<addr>&port=<n>&edition=bedrock|java     -> one probe, cached 60 s
//	GET /ping?host=<name>&family=4|6&port=<n>&edition=... -> the same, resolved here
//	GET /health
//
// Environment: PORT, ALLOWED_ORIGINS, FILTER_INTERNAL_TARGETS (default true).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	resolveTimeout = 5 * time.Second
	pingTimeout    = 8 * time.Second
	maxHostLen     = 253
	healthInterval = 7 * time.Second
)

// version is set at build time: -ldflags "-X main.version=v1.2".
var version = "dev"

// filterInternal keeps probes off internal addresses, see internalTarget.
var filterInternal = true

// ipv6Egress caches hasGlobalIPv6. A ticker refreshes it every
// healthInterval, so /health requests only read it.
var ipv6Egress atomic.Bool

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	origins := strings.Split(os.Getenv("ALLOWED_ORIGINS"), ",")
	if v, err := strconv.ParseBool(os.Getenv("FILTER_INTERNAL_TARGETS")); err == nil {
		filterInternal = v
	}

	ipv6Egress.Store(hasGlobalIPv6())
	go func() {
		for range time.Tick(healthInterval) {
			ipv6Egress.Store(hasGlobalIPv6())
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/ping", endpoint(origins, handlePing))
	mux.HandleFunc("/health", endpoint(origins, handleHealth))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httpError(w, http.StatusNotFound, "unknown endpoint")
	})

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      pingTimeout + 2*time.Second,
	}
	log.Printf("%s listening on :%s (ipv6 egress: %v, internal targets filtered: %v)",
		version, port, ipv6Egress.Load(), filterInternal)
	log.Fatal(srv.ListenAndServe())
}

// validHost reports whether host can be a DNS name.
func validHost(host string) bool {
	return host != "" && len(host) <= maxHostLen && !strings.ContainsAny(host, " /\\@#?:[]")
}

// resolveFamily returns the first address of host in family ("4" or "6"),
// nil when host has no record there.
func resolveFamily(ctx context.Context, host, family string) (net.IP, error) {
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip"+family, host)
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return nil, nil
	}
	if err != nil || len(ips) == 0 {
		return nil, err
	}
	return ips[0], nil
}

func lookupReason(err error) string {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsTimeout {
		return "timeout"
	}
	return "lookup failed"
}

func handlePing(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	edition := q.Get("edition")
	if edition == "" {
		edition = "bedrock"
	}
	if _, ok := editionNetworks[edition]; !ok {
		httpError(w, http.StatusBadRequest, "edition must be bedrock or java")
		return
	}
	port, err := strconv.Atoi(q.Get("port"))
	if err != nil || port < 1 || port > 65535 {
		httpError(w, http.StatusBadRequest, "port must be between 1 and 65535")
		return
	}
	hostLabel := strings.TrimSpace(q.Get("host"))
	var ip net.IP
	family := ""
	if raw := q.Get("ip"); raw != "" {
		if ip = net.ParseIP(strings.Trim(raw, "[]")); ip == nil {
			httpError(w, http.StatusBadRequest, "ip must be a literal IPv4 or IPv6 address")
			return
		}
		if filterInternal && internalTarget(ip) {
			httpError(w, http.StatusBadRequest, "target address is not public")
			return
		}
		if len(hostLabel) > maxHostLen {
			hostLabel = ""
		}
	} else {
		if !validHost(hostLabel) {
			httpError(w, http.StatusBadRequest, "ip or host is missing or invalid")
			return
		}
		if family = q.Get("family"); family != "4" && family != "6" {
			httpError(w, http.StatusBadRequest, "family must be 4 or 6 when host is given without ip")
			return
		}
	}
	// The system a client is charged for: the name it asked about, or the
	// literal address. Both families and all port fallbacks count once.
	system := hostLabel
	if system == "" {
		system = ip.String()
	}
	if wait, reason := limits.admitProbe(clientIP(r), system); limited(w, wait, reason) {
		return
	}

	if ip == nil {
		ip, err = resolveFamily(r.Context(), hostLabel, family)
		switch {
		case err != nil:
			writeJSON(w, PingResult{State: "dns_error", Error: lookupReason(err)})
			return
		case ip == nil:
			writeJSON(w, PingResult{State: "no_dns"})
			return
		case filterInternal && internalTarget(ip):
			httpError(w, http.StatusBadRequest, "target address is not public")
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), pingTimeout)
	defer cancel()
	key := edition + "|" + ip.String() + "|" + strconv.Itoa(port)
	res := cache.get(ctx, key, func() (PingResult, bool) {
		if !acquireProbe() {
			return PingResult{State: "busy"}, false
		}
		defer releaseProbe()
		return ping(ctx, edition, ip, port, hostLabel), true
	})
	if res.State == "busy" {
		w.Header().Set("Retry-After", "5")
		httpError(w, http.StatusServiceUnavailable, "checker busy, try again shortly")
		return
	}
	res.IP = ip.String()
	writeJSON(w, res)
}

var limitMessages = map[string]string{
	"systems": "More than 10 systems within a minute, cooling down",
	"health":  "More than 4 health requests within 7 seconds, cooling down",
	"rate":    "Too many requests, slow down",
}

// limited answers 429 when the client is over a limit.
func limited(w http.ResponseWriter, wait int, reason string) bool {
	if wait == 0 {
		return false
	}
	w.Header().Set("Retry-After", strconv.Itoa(wait))
	httpError(w, http.StatusTooManyRequests, limitMessages[reason])
	return true
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	if wait, reason := limits.admitHealth(clientIP(r)); limited(w, wait, reason) {
		return
	}
	writeJSON(w, map[string]any{"ok": true, "version": version, "ipv6": ipv6Egress.Load()})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// hasGlobalIPv6 reports whether any interface carries a global unicast IPv6
// address. It is a hint only; the real test is a ping against a v6 target.
func hasGlobalIPv6() bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() == nil && ipn.IP.IsGlobalUnicast() {
			return true
		}
	}
	return false
}

// -- CORS --------------------------------------------------------

// endpoint answers GET and HEAD, everything else with a JSON 405.
func endpoint(allowed []string, next http.HandlerFunc) http.HandlerFunc {
	return withCORS(allowed, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			httpError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		next(w, r)
	})
}

func withCORS(allowed []string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && originAllowed(allowed, origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		next(w, r)
	}
}

func originAllowed(allowed []string, origin string) bool {
	for _, a := range allowed {
		a = strings.TrimSpace(a)
		if a == "*" || strings.EqualFold(a, origin) {
			return true
		}
	}
	return false
}

// clientIP is the first X-Forwarded-For entry. The relay on the web host
// sets it, and Caddy only accepts forwarded headers from that host, so
// the chain is trusted end to end. Without the header: the peer address.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.SplitN(xff, ",", 2)[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
