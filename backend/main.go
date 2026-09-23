// SPDX-License-Identifier: AGPL-3.0-or-later

// mc_dualstack_check backend. Two network primitives the browser cannot do
// itself; everything else (fallback order, dual-stack logic, rendering)
// lives in the frontend. The API is documented in docs/api.md.
//
//	GET /resolve?host=<name>                          -> A and AAAA records
//	GET /ping?ip=<addr>&port=<n>&edition=bedrock|java -> one probe, cached 60 s
//	GET /health
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
	"time"
)

const (
	resolveTimeout = 5 * time.Second
	pingTimeout    = 8 * time.Second
	maxHostLen     = 253
)

// version is set at build time: -ldflags "-X main.version=v1.2".
var version = "dev"

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	origins := strings.Split(os.Getenv("ALLOWED_ORIGINS"), ",")

	mux := http.NewServeMux()
	mux.HandleFunc("/resolve", endpoint(origins, handleResolve))
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
	log.Printf("%s listening on :%s (ipv6 egress: %v)", version, port, hasGlobalIPv6())
	log.Fatal(srv.ListenAndServe())
}

func handleResolve(w http.ResponseWriter, r *http.Request) {
	host := strings.TrimSpace(r.URL.Query().Get("host"))
	if host == "" || len(host) > maxHostLen || strings.ContainsAny(host, " /\\@#?:[]") {
		httpError(w, http.StatusBadRequest, "host is missing or invalid")
		return
	}
	if !admit(w, r, "") {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), resolveTimeout)
	defer cancel()

	var out resolveResult
	var err4, err6 error
	out.A, err4 = lookup(ctx, "ip4", host)
	out.AAAA, err6 = lookup(ctx, "ip6", host)
	if err4 != nil || err6 != nil {
		out.Errors = map[string]string{}
		if err4 != nil {
			out.Errors["a"] = lookupReason(err4)
		}
		if err6 != nil {
			out.Errors["aaaa"] = lookupReason(err6)
		}
	}
	writeJSON(w, out)
}

// resolveResult keeps "no record" (empty list) apart from "lookup failed"
// (entry in Errors), so the frontend never reports a failed lookup as a
// missing record.
type resolveResult struct {
	A      []string          `json:"a"`
	AAAA   []string          `json:"aaaa"`
	Errors map[string]string `json:"errors,omitempty"`
}

func lookup(ctx context.Context, network, host string) ([]string, error) {
	addrs, err := net.DefaultResolver.LookupIP(ctx, network, host)
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		err = nil
	}
	out := []string{}
	for _, a := range addrs {
		out = append(out, a.String())
	}
	return out, err
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
	// The system a client is charged for: the name it asked about, or the
	// literal address. Both families and all port fallbacks count once.
	system := hostLabel
	if system == "" {
		system = ip.String()
	}
	if !admit(w, r, system) {
		return
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
	writeJSON(w, res)
}

// admit applies the per-client limits and answers 429 when one is hit.
func admit(w http.ResponseWriter, r *http.Request, system string) bool {
	wait, reason := limits.admit(clientIP(r), system)
	if wait == 0 {
		return true
	}
	w.Header().Set("Retry-After", strconv.Itoa(wait))
	if reason == "cooldown" {
		httpError(w, http.StatusTooManyRequests, "More than 10 systems within a minute, cooling down")
	} else {
		httpError(w, http.StatusTooManyRequests, "Too many requests, slow down")
	}
	return false
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"ok": true, "version": version, "ipv6": hasGlobalIPv6()})
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
