// SPDX-License-Identifier: AGPL-3.0-or-later

// mc_dualstack_check backend. It does the network work the browser cannot:
// a name lookup per address family and a Minecraft status probe. Everything
// else (fallback order, dual-stack logic, rendering) lives in the frontend.
// The API is documented in docs/api.md.
//
//	GET /ping?ip=<addr>&port=<n>&edition=bedrock|java     -> one probe, cached 60 s
//	GET /ping?host=<name>&family=4|6&port=<n>&edition=... -> the same, resolved here
//	    &id=<connection id>                               -> Java only
//	GET /health
//
// Listens on loopback only; Caddy in front is the public side.
// Environment: PORT, ALLOWED_ORIGINS, FILTER_INTERNAL_TARGETS (default true),
// STATE_DIRECTORY (set by systemd; where the usage numbers are written).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// A name lookup and a probe together stay within 10 s. The SRV lookup is
// part of the name lookup's time.
const (
	resolveTimeout = 4 * time.Second
	srvTimeout     = 2 * time.Second
	srvWait        = srvTimeout + 500*time.Millisecond
	pingTimeout    = 6 * time.Second
	maxHostLen     = 253
	maxIDLen       = 64
	healthInterval = 7 * time.Second
)

// version is set at build time: -ldflags "-X main.version=v1.2".
var version = "dev"

// filterInternal keeps probes off internal addresses, see internalTarget.
var filterInternal = true

// ipv6Egress caches hasGlobalIPv6. A ticker refreshes it every
// healthInterval, so /health requests only read it. The same tick sweeps
// the result cache and the client table.
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
			cache.sweep()
			limits.sweep()
		}
	}()

	statsDir := os.Getenv("STATE_DIRECTORY")
	stop, done := make(chan struct{}), make(chan struct{})
	go runStats(loadUsage(statsDir), statsDir, stop, done)
	// On stop or restart the usage numbers are written once more.
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, os.Interrupt)
		<-sig
		close(stop)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
		os.Exit(0)
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/ping", endpoint(origins, handlePing))
	mux.HandleFunc("/health", endpoint(origins, handleHealth))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httpError(w, http.StatusNotFound, "unknown endpoint")
	})

	srv := &http.Server{
		Addr:              "127.0.0.1:" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      resolveTimeout + pingTimeout + 2*time.Second,
		// Longer than the reverse proxy keeps idle connections (2 min in
		// Caddy), so the proxy always closes first.
		IdleTimeout:    3 * time.Minute,
		MaxHeaderBytes: 16 << 10,
	}
	log.Printf("%s listening on %s (ipv6 egress: %v, internal targets filtered: %v)",
		version, srv.Addr, ipv6Egress.Load(), filterInternal)
	log.Fatal(srv.ListenAndServe())
}

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

// connectionID reports whether id can match an entry of a server's
// allowed-connection-ids. The server splits that list at commas and trims
// the entries.
func connectionID(id string) bool {
	if id == "" || len(id) > maxIDLen || id[0] == ' ' || id[len(id)-1] == ' ' {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] < ' ' || id[i] > '~' || id[i] == ',' {
			return false
		}
	}
	return true
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

func handlePing(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	edName := q.Get("edition")
	if edName == "" {
		edName = defaultEdition
	}
	ed, ok := editions[edName]
	if !ok {
		httpError(w, http.StatusBadRequest, editionError)
		return
	}
	port, err := strconv.Atoi(q.Get("port"))
	if err != nil || port < 1 || port > 65535 {
		httpError(w, http.StatusBadRequest, "port must be between 1 and 65535")
		return
	}
	id := q.Get("id")
	switch {
	case id != "" && !ed.connectionIDs:
		httpError(w, http.StatusBadRequest, "id is for java only")
		return
	case id != "" && !connectionID(id):
		httpError(w, http.StatusBadRequest, "id must be 1 to "+strconv.Itoa(maxIDLen)+" printable ASCII characters, no comma, no space at either end")
		return
	}
	// With ip, an invalid host is dropped; without, it is an error.
	host := hostName(strings.TrimSpace(q.Get("host")))
	var ip net.IP
	family := ""
	if raw := q.Get("ip"); raw != "" {
		lit := strings.Trim(raw, "[]")
		if ip = net.ParseIP(lit); ip == nil {
			httpError(w, http.StatusBadRequest, "ip must be a literal IPv4 or IPv6 address")
			return
		}
		// It would be probed over IPv4 while asked for as IPv6.
		if ip.To4() != nil && strings.Contains(lit, ":") {
			httpError(w, http.StatusBadRequest, "ip is an IPv4-mapped IPv6 address; give the IPv4 address")
			return
		}
		if filterInternal && internalTarget(ip) {
			httpError(w, http.StatusBadRequest, "target address is not public")
			return
		}
	} else {
		if host == "" {
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
	system := host
	if system == "" {
		system = ip.String()
	}
	client := clientKey(r)
	if wait, reason := limits.admitProbe(client, system); limited(w, wait, reason) {
		return
	}
	// Counted once answered; cached when the answer is the cached result.
	cached := false
	defer func() { countPing(client, cached) }()

	// The name a client would send: the SRV target when there is one.
	label := host
	var srv *SRVTarget
	if ip == nil {
		// A lookup takes one of the in-flight slots while it runs.
		if !acquireSlot() {
			busy(w)
			return
		}
		lctx, cancel := context.WithTimeout(r.Context(), resolveTimeout)
		if ed.srvPort != 0 && port == ed.srvPort {
			if srv = lookupSRV(lctx, ed.srvService, ed.network, host); srv != nil {
				label, port = srv.Host, srv.Port
			}
		}
		ip, err = resolveFamily(lctx, label, family)
		cancel()
		releaseSlot()
		switch {
		case err != nil:
			writeJSON(w, PingResult{State: "dns_error", Error: lookupReason(err), SRV: srv})
			return
		case ip == nil:
			writeJSON(w, PingResult{State: "no_dns", SRV: srv})
			return
		}
	}

	// The probe may be shared and its result cached, so it does not end
	// when this client goes away.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), pingTimeout)
	defer cancel()
	// Proxies answer per name, and a server with connection IDs answers
	// only the right one: a result is shared only with the same name and ID.
	key := strings.Join([]string{edName, ip.String(), strconv.Itoa(port), label, id}, "|")
	res := cache.get(ctx, key, func() (PingResult, bool) {
		if !acquireSlot() {
			return PingResult{State: "busy"}, false
		}
		defer releaseSlot()
		return ping(ctx, ed, target{ip: ip, port: port, host: label, id: id}), true
	})
	cached = res.Cached
	if res.State == "busy" {
		busy(w)
		return
	}
	res.IP = ip.String()
	res.SRV = srv
	writeJSON(w, res)
}

// busy answers that all in-flight slots are taken.
func busy(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "5")
	httpError(w, http.StatusServiceUnavailable, "checker busy, try again shortly")
}

var limitMessages = map[string]string{
	"systems": fmt.Sprintf("More than %d systems within %d seconds", systemsMax, int(systemsWindow.Seconds())),
	"health":  fmt.Sprintf("More than %d health requests within %d seconds, cooling down", healthMax, int(healthInterval.Seconds())),
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
	if wait, reason := limits.admitHealth(clientKey(r)); limited(w, wait, reason) {
		return
	}
	writeJSON(w, map[string]any{"ok": true, "version": version, "ipv6": ipv6Egress.Load()})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// hasGlobalIPv6 reports whether any interface carries a global unicast IPv6
// address. Unique local addresses (fc00::/7) do not count. It is a hint
// only; the real test is a ping against a v6 target.
func hasGlobalIPv6() bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() == nil && ipn.IP.IsGlobalUnicast() && !ipn.IP.IsPrivate() {
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
			// The web interface reads it for its 429 countdown.
			w.Header().Set("Access-Control-Expose-Headers", "Retry-After")
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

// clientKey is the address a client is charged for: the first
// X-Forwarded-For entry, or the peer address when that is no address.
// Caddy accepts forwarded headers from the web host only, so clients cannot
// forge the entry. IPv6 clients are keyed by their /64, since one host
// usually holds a whole /64.
func clientKey(r *http.Request) string {
	a, err := netip.ParseAddr(strings.TrimSpace(strings.SplitN(r.Header.Get("X-Forwarded-For"), ",", 2)[0]))
	if err != nil {
		ap, err := netip.ParseAddrPort(r.RemoteAddr)
		if err != nil {
			return r.RemoteAddr
		}
		a = ap.Addr()
	}
	a = a.Unmap().WithZone("")
	if a.Is6() {
		p, _ := a.Prefix(64)
		return p.String()
	}
	return a.String()
}
