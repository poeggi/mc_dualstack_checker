// mc_dualstack_check backend. Two network primitives the browser cannot do
// itself; everything else (fallback order, dual-stack logic, rendering)
// lives in the frontend.
//
//	GET /resolve?host=<name>                          -> A and AAAA records
//	GET /ping?ip=<addr>&port=<n>&edition=bedrock|java -> one probe
//	GET /healthz
package main

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	resolveTimeout = 5 * time.Second
	pingTimeout    = 8 * time.Second
	maxHostLen     = 253
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	origins := strings.Split(os.Getenv("ALLOWED_ORIGINS"), ",")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /resolve", withCORS(origins, rateLimited(handleResolve)))
	mux.HandleFunc("GET /ping", withCORS(origins, rateLimited(handlePing)))
	mux.HandleFunc("GET /healthz", withCORS(origins, handleHealth))

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      pingTimeout + 2*time.Second,
	}
	log.Printf("listening on :%s (ipv6 egress: %v)", port, hasGlobalIPv6())
	log.Fatal(srv.ListenAndServe())
}

func handleResolve(w http.ResponseWriter, r *http.Request) {
	host := strings.TrimSpace(r.URL.Query().Get("host"))
	if host == "" || len(host) > maxHostLen || strings.ContainsAny(host, " /\\@#?:[]") {
		httpError(w, http.StatusBadRequest, "host is missing or invalid")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), resolveTimeout)
	defer cancel()

	out := map[string][]string{"a": {}, "aaaa": {}}
	if addrs, err := net.DefaultResolver.LookupIP(ctx, "ip4", host); err == nil {
		for _, a := range addrs {
			out["a"] = append(out["a"], a.String())
		}
	}
	if addrs, err := net.DefaultResolver.LookupIP(ctx, "ip6", host); err == nil {
		for _, a := range addrs {
			out["aaaa"] = append(out["aaaa"], a.String())
		}
	}
	writeJSON(w, out)
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
	ip := net.ParseIP(strings.Trim(q.Get("ip"), "[]"))
	if ip == nil {
		httpError(w, http.StatusBadRequest, "ip must be a literal IPv4 or IPv6 address")
		return
	}
	port, err := strconv.Atoi(q.Get("port"))
	if err != nil || port < 1 || port > 65535 {
		httpError(w, http.StatusBadRequest, "port must be between 1 and 65535")
		return
	}
	hostLabel := q.Get("host")
	if len(hostLabel) > maxHostLen {
		hostLabel = ""
	}

	ctx, cancel := context.WithTimeout(r.Context(), pingTimeout)
	defer cancel()
	writeJSON(w, ping(ctx, edition, ip, port, hostLabel))
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"ok": true, "ipv6": hasGlobalIPv6()})
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

// -- Rate limit --------------------------------------------------
// Token bucket per client IP, in memory. A full dual-stack check from the
// frontend costs one resolve plus up to three pings per family.

const (
	rlBurst  = 15
	rlRefill = time.Second
)

type bucket struct {
	tokens float64
	last   time.Time
}

var (
	rlMu      sync.Mutex
	rlBuckets = map[string]*bucket{}
	rlSweep   = time.Now()
)

func rateLimited(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		now := time.Now()

		rlMu.Lock()
		if now.Sub(rlSweep) > 10*time.Minute {
			for k, b := range rlBuckets {
				if now.Sub(b.last) > 10*time.Minute {
					delete(rlBuckets, k)
				}
			}
			rlSweep = now
		}
		b, ok := rlBuckets[ip]
		if !ok {
			b = &bucket{tokens: rlBurst, last: now}
			rlBuckets[ip] = b
		}
		b.tokens += now.Sub(b.last).Seconds() / rlRefill.Seconds()
		if b.tokens > rlBurst {
			b.tokens = rlBurst
		}
		b.last = now
		allowed := b.tokens >= 1
		if allowed {
			b.tokens--
		}
		wait := int((1 - b.tokens) * rlRefill.Seconds())
		rlMu.Unlock()

		if !allowed {
			if wait < 1 {
				wait = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(wait))
			httpError(w, http.StatusTooManyRequests, "Too many requests, slow down")
			return
		}
		next(w, r)
	}
}

// clientIP takes the last X-Forwarded-For entry, which is the one appended
// by the trusted proxy in front of this service (Cloud Run does this).
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[len(parts)-1])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
