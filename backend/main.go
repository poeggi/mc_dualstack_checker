// mc_dualstack_check backend: pings a Minecraft server over IPv4 and IPv6
// separately and reports both results as JSON.
//
//	GET /check?host=<name|ip>&edition=bedrock|java&port4=&port6=&nofallback=1
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
	checkTimeout = 25 * time.Second
	maxHostLen   = 253
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	origins := strings.Split(os.Getenv("ALLOWED_ORIGINS"), ",")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /check", withCORS(origins, rateLimited(handleCheck)))
	mux.HandleFunc("GET /healthz", withCORS(origins, handleHealth))

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      checkTimeout + 5*time.Second,
	}
	log.Printf("listening on :%s (ipv6 egress: %v)", port, hasGlobalIPv6())
	log.Fatal(srv.ListenAndServe())
}

func handleCheck(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	req := CheckRequest{
		Host:       strings.TrimSpace(q.Get("host")),
		Edition:    q.Get("edition"),
		NoFallback: q.Get("nofallback") != "",
	}
	if req.Edition == "" {
		req.Edition = "bedrock"
	}
	if _, ok := defaultPorts[req.Edition]; !ok {
		httpError(w, http.StatusBadRequest, "edition must be bedrock or java")
		return
	}
	if req.Host == "" || len(req.Host) > maxHostLen || strings.ContainsAny(req.Host, " /\\@#?") {
		httpError(w, http.StatusBadRequest, "host is missing or invalid")
		return
	}
	var err error
	if req.Port4, err = parsePort(q.Get("port4")); err != nil {
		httpError(w, http.StatusBadRequest, "IPv4 "+err.Error())
		return
	}
	if req.Port6, err = parsePort(q.Get("port6")); err != nil {
		httpError(w, http.StatusBadRequest, "IPv6 "+err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), checkTimeout)
	defer cancel()
	resp := runCheck(ctx, req)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(resp)
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":   true,
		"ipv6": hasGlobalIPv6(),
	})
}

func parsePort(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return 0, errPort
	}
	return n, nil
}

var errPort = &portError{}

type portError struct{}

func (*portError) Error() string { return "port must be between 1 and 65535" }

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// hasGlobalIPv6 reports whether any interface carries a global unicast IPv6
// address. It is a hint only; the real test is a check against a v6 target.
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
// Token bucket per client IP, in memory. Each instance keeps its own state,
// which is enough to stop a single client from hammering the backend.

const (
	rlBurst  = 5
	rlRefill = 3 * time.Second
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
