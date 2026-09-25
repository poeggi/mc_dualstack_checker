// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// Anonymous usage numbers: probe requests and unique clients per minute,
// hour, day and month (UTC), each for IPv4 and IPv6 clients. Unique
// clients are counted with HyperLogLog sketches, which keep no addresses.
// One worker owns all numbers. Requests hand it a sample without waiting;
// once a minute it writes stats.json, which Caddy serves as a static file,
// and its own state, so numbers survive restarts.

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"hash/fnv"
	"log"
	"math"
	"math/bits"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	statsInterval = time.Minute
	statsPublic   = "stats.json"
	statsPrivate  = "stats.state"
	hllP          = 12 // 4096 registers: 4 KB per sketch, about 1.6 % error
	hllM          = 1 << hllP
)

// sketch is a HyperLogLog register set.
type sketch []byte

func newSketch() sketch { return make(sketch, hllM) }

func (s sketch) add(h uint64) {
	i := h >> (64 - hllP)
	rank := uint8(bits.LeadingZeros64(h<<hllP|1<<(hllP-1))) + 1
	if rank > s[i] {
		s[i] = rank
	}
}

func (s sketch) estimate() uint64 {
	m := float64(hllM)
	sum, zeros := 0.0, 0
	for _, r := range s {
		sum += math.Ldexp(1, -int(r))
		if r == 0 {
			zeros++
		}
	}
	e := 0.7213 / (1 + 1.079/m) * m * m / sum
	if e <= 2.5*m && zeros > 0 {
		e = m * math.Log(m/float64(zeros))
	}
	return uint64(e + 0.5)
}

type counts struct {
	Requests uint64 `json:"requests"`
	Clients  uint64 `json:"clients"`
}

type bucket struct {
	Start   time.Time `json:"start"`
	Current bool      `json:"current,omitempty"`
	IPv4    counts    `json:"ipv4"`
	IPv6    counts    `json:"ipv6"`
}

// series is one kind of period: the running bucket with its sketches, and
// the finished buckets, newest first.
type series struct {
	Cur    bucket
	Sketch [2]sketch
	Done   []bucket
}

type periodKind struct {
	name  string
	keep  int // buckets shown, the running one included
	start func(time.Time) time.Time
	next  func(time.Time) time.Time
}

var periodKinds = []periodKind{
	{"minutes", 60, func(t time.Time) time.Time { return t.Truncate(time.Minute) },
		func(t time.Time) time.Time { return t.Add(time.Minute) }},
	{"hours", 24, func(t time.Time) time.Time { return t.Truncate(time.Hour) },
		func(t time.Time) time.Time { return t.Add(time.Hour) }},
	{"days", 30, func(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC) },
		func(t time.Time) time.Time { return t.AddDate(0, 0, 1) }},
	{"months", 12, func(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC) },
		func(t time.Time) time.Time { return t.AddDate(0, 1, 0) }},
}

// usage is the worker's state; only the worker touches it.
type usage struct {
	Salt   uint64
	Series map[string]*series
}

type sample struct {
	v6  bool
	key string
}

var samples = make(chan sample, 1024)

// countPing hands one admitted probe request to the stats worker. It never
// waits: when the worker is behind, the sample is dropped.
func countPing(client string) {
	select {
	case samples <- sample{strings.Contains(client, ":"), client}:
	default:
	}
}

func newUsage() *usage {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return &usage{Salt: binary.LittleEndian.Uint64(b[:]), Series: map[string]*series{}}
}

// loadUsage reads the state kept in dir, or starts empty.
func loadUsage(dir string) *usage {
	u := newUsage()
	if dir == "" {
		return u
	}
	b, err := os.ReadFile(filepath.Join(dir, statsPrivate))
	if err != nil {
		return u
	}
	var kept usage
	if json.Unmarshal(b, &kept) != nil || kept.Series == nil {
		return u
	}
	u.Salt = kept.Salt
	for _, k := range periodKinds {
		s := kept.Series[k.name]
		if s == nil {
			continue
		}
		for f := range s.Sketch {
			if len(s.Sketch[f]) != hllM {
				s.Sketch[f] = newSketch()
			}
		}
		u.Series[k.name] = s
	}
	return u
}

func (u *usage) hash(key string) uint64 {
	h := fnv.New64a()
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], u.Salt)
	h.Write(b[:])
	h.Write([]byte(key))
	x := h.Sum64()
	x ^= x >> 33
	x *= 0xff51afd7ed558ccd
	x ^= x >> 33
	x *= 0xc4ceb9fe1a85ec53
	return x ^ x>>33
}

// roll finishes every running bucket whose period has ended. Periods
// without requests become empty buckets.
func (u *usage) roll(now time.Time) {
	now = now.UTC()
	for _, k := range periodKinds {
		s := u.Series[k.name]
		if s == nil {
			s = &series{Sketch: [2]sketch{newSketch(), newSketch()}}
			u.Series[k.name] = s
		}
		start := k.start(now)
		if s.Cur.Start.Equal(start) {
			continue
		}
		if !s.Cur.Start.IsZero() {
			s.Done = append([]bucket{s.finished()}, s.Done...)
			for t := k.next(s.Cur.Start); t.Before(start) && len(s.Done) < k.keep; t = k.next(t) {
				s.Done = append([]bucket{{Start: t}}, s.Done...)
			}
		}
		if len(s.Done) > k.keep-1 {
			s.Done = s.Done[:k.keep-1]
		}
		s.Cur = bucket{Start: start}
		s.Sketch = [2]sketch{newSketch(), newSketch()}
	}
}

// finished is the running bucket with its client estimates.
func (s *series) finished() bucket {
	b := s.Cur
	b.IPv4.Clients = s.Sketch[0].estimate()
	b.IPv6.Clients = s.Sketch[1].estimate()
	return b
}

func (u *usage) add(now time.Time, smp sample) {
	u.roll(now)
	h := u.hash(smp.key)
	for _, k := range periodKinds {
		s := u.Series[k.name]
		if smp.v6 {
			s.Cur.IPv6.Requests++
			s.Sketch[1].add(h)
		} else {
			s.Cur.IPv4.Requests++
			s.Sketch[0].add(h)
		}
	}
}

// public is the content of stats.json: per kind of period, the running
// bucket first, then the finished ones.
func (u *usage) public(now time.Time) []byte {
	out := map[string]any{"updated": now.UTC().Truncate(time.Second)}
	for _, k := range periodKinds {
		s := u.Series[k.name]
		cur := s.finished()
		cur.Current = true
		out[k.name] = append([]bucket{cur}, s.Done...)
	}
	b, _ := json.Marshal(out)
	return b
}

// writeFile replaces path in one step: readers see the old or the new
// file, never a partial one.
func writeFile(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (u *usage) publish(dir string, now time.Time) {
	u.roll(now)
	if dir == "" {
		return
	}
	kept, _ := json.Marshal(u)
	if err := writeFile(filepath.Join(dir, statsPrivate), kept, 0o600); err != nil {
		log.Printf("stats: %v", err)
		return
	}
	if err := writeFile(filepath.Join(dir, statsPublic), u.public(now), 0o644); err != nil {
		log.Printf("stats: %v", err)
	}
}

// runStats counts samples and writes the numbers to dir at start, once per
// statsInterval, and when stop closes; then it closes done.
func runStats(u *usage, dir string, stop <-chan struct{}, done chan<- struct{}) {
	u.publish(dir, time.Now())
	tick := time.NewTicker(statsInterval)
	defer tick.Stop()
	for {
		select {
		case smp := <-samples:
			u.add(time.Now(), smp)
		case <-tick.C:
			u.publish(dir, time.Now())
		case <-stop:
			u.publish(dir, time.Now())
			close(done)
			return
		}
	}
}
