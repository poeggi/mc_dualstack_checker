// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// Anonymous usage numbers: probe requests and unique clients per minute,
// hour, day and month (UTC), each for IPv4 and IPv6 clients. Unique
// clients are counted with HyperLogLog sketches, which keep no addresses.
// One worker owns all numbers. Requests hand it a sample without waiting.
// Only finished periods are published, one file per kind of period under
// stats/, rewritten when a period of that kind ends; Caddy serves them as
// static files. The worker's own state is written after new requests, so
// the numbers survive restarts.

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
	statsPublic  = "stats"
	statsPrivate = "stats.state"
	hllP         = 12 // 4096 registers: 4 KB per sketch, about 1.6 % error
	hllM         = 1 << hllP
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
	Start time.Time `json:"start"`
	IPv4  counts    `json:"ipv4"`
	IPv6  counts    `json:"ipv6"`
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
	keep  int // finished buckets published
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

// usage is the worker's state; only the worker touches it. rolled names
// the kinds whose published file is due; dirty is set by new requests.
type usage struct {
	Salt   uint64
	Series map[string]*series
	rolled map[string]bool
	dirty  bool
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
	return &usage{Salt: binary.LittleEndian.Uint64(b[:]), Series: map[string]*series{}, rolled: map[string]bool{}}
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
			// Missed periods, newest last; only the newest keep matter.
			var missed []bucket
			for t := k.next(s.Cur.Start); t.Before(start); t = k.next(t) {
				missed = append(missed, bucket{Start: t})
				if len(missed) > k.keep {
					missed = missed[1:]
				}
			}
			for _, b := range missed {
				s.Done = append([]bucket{b}, s.Done...)
			}
		}
		if len(s.Done) > k.keep {
			s.Done = s.Done[:k.keep]
		}
		s.Cur = bucket{Start: start}
		s.Sketch = [2]sketch{newSketch(), newSketch()}
		u.rolled[k.name] = true
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
	u.dirty = true
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

// public is the published file of one kind: its finished periods, newest
// first.
func (u *usage) public(kind string, now time.Time) []byte {
	periods := append([]bucket{}, u.Series[kind].Done...)
	b, _ := json.Marshal(map[string]any{
		"updated": now.UTC().Truncate(time.Second),
		"periods": periods,
	})
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

// publish writes the state when there were new requests, and the file of
// every kind whose period ended.
func (u *usage) publish(dir string, now time.Time) {
	u.roll(now)
	if dir == "" {
		clear(u.rolled)
		return
	}
	if u.dirty {
		kept, _ := json.Marshal(u)
		if err := writeFile(filepath.Join(dir, statsPrivate), kept, 0o600); err != nil {
			log.Printf("stats: %v", err)
			return
		}
		u.dirty = false
	}
	for name := range u.rolled {
		if err := writeFile(filepath.Join(dir, statsPublic, name+".json"), u.public(name, now), 0o644); err != nil {
			log.Printf("stats: %v", err)
			return
		}
		delete(u.rolled, name)
	}
}

// untilNextMinute is the wait until just after the next full minute.
func untilNextMinute(now time.Time) time.Duration {
	return now.Truncate(time.Minute).Add(time.Minute + time.Second).Sub(now)
}

// runStats counts samples and publishes at start, just after every full
// minute, and when stop closes; then it closes done.
func runStats(u *usage, dir string, stop <-chan struct{}, done chan<- struct{}) {
	if dir != "" {
		if err := os.MkdirAll(filepath.Join(dir, statsPublic), 0o755); err != nil {
			log.Printf("stats: %v", err)
		}
	}
	for _, k := range periodKinds {
		u.rolled[k.name] = true
	}
	u.publish(dir, time.Now())
	tick := time.NewTimer(untilNextMinute(time.Now()))
	defer tick.Stop()
	for {
		select {
		case smp := <-samples:
			u.add(time.Now(), smp)
		case <-tick.C:
			u.publish(dir, time.Now())
			tick.Reset(untilNextMinute(time.Now()))
		case <-stop:
			u.publish(dir, time.Now())
			close(done)
			return
		}
	}
}
