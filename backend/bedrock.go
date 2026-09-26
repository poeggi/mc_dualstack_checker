// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"strconv"
	"strings"
	"time"
)

// RakNet offline message ID; identifies unconnected ping/pong frames.
var raknetMagic = []byte{0x00, 0xff, 0xff, 0x00, 0xfe, 0xfe, 0xfe, 0xfe, 0xfd, 0xfd, 0xfd, 0xfd, 0x12, 0x34, 0x56, 0x78}

const (
	raknetUnconnectedPing = 0x01
	raknetUnconnectedPong = 0x1c
	bedrockAttempts       = 2
	bedrockReplyTimeout   = 2 * time.Second
)

// pingBedrock sends a RakNet unconnected ping over the given network
// ("udp4" or "udp6") and parses the pong string. The ping carries no name.
// The round trip includes a lost first attempt.
func pingBedrock(ctx context.Context, network string, t target) (*ServerInfo, time.Duration, error) {
	start := time.Now()
	addr := net.JoinHostPort(t.ip.String(), strconv.Itoa(t.port))
	conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
	if err != nil {
		return nil, 0, err
	}
	defer conn.Close()
	defer context.AfterFunc(ctx, func() { conn.Close() })()

	req := make([]byte, 0, 1+8+16+8)
	req = append(req, raknetUnconnectedPing)
	req = binary.BigEndian.AppendUint64(req, uint64(time.Now().UnixMilli()))
	req = append(req, raknetMagic...)
	req = binary.BigEndian.AppendUint64(req, 0x4d43444b43484b) // arbitrary client GUID

	buf := make([]byte, 4096)
	var lastErr error
	for attempt := 0; attempt < bedrockAttempts; attempt++ {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		if _, err := conn.Write(req); err != nil {
			return nil, 0, err
		}
		deadline := time.Now().Add(bedrockReplyTimeout)
		if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
			deadline = d
		}
		_ = conn.SetReadDeadline(deadline)
		n, err := conn.Read(buf)
		if err != nil {
			lastErr = err
			continue
		}
		info, perr := parsePong(buf[:n])
		if perr != nil {
			lastErr = perr
			continue
		}
		return info, time.Since(start), nil
	}
	return nil, 0, lastErr
}

// parsePong decodes an unconnected pong:
// 0x1c | time(8) | server GUID(8) | magic(16) | strlen(2) | payload.
func parsePong(b []byte) (*ServerInfo, error) {
	const hdr = 1 + 8 + 8 + 16 + 2
	if len(b) < hdr || b[0] != raknetUnconnectedPong {
		return nil, probeError("not an unconnected pong")
	}
	if !bytes.Equal(b[17:33], raknetMagic) {
		return nil, probeError("bad raknet magic")
	}
	slen := int(binary.BigEndian.Uint16(b[33:35]))
	if hdr+slen > len(b) {
		slen = len(b) - hdr
	}
	payload := string(b[hdr : hdr+slen])

	// MCPE;MOTD;protocol;version;players;max;serverid;level;gamemode;gamemodeNum;port4;port6;
	f := strings.Split(payload, ";")
	get := func(i int) string {
		if i < len(f) {
			return strings.TrimSpace(f[i])
		}
		return ""
	}
	info := &ServerInfo{
		Edition:  get(0),
		MOTD:     stripFormatting(get(1)),
		MOTDRaw:  formatted(get(1)),
		Protocol: get(2),
		Version:  get(3),
		ServerID: get(6),
		Map:      stripFormatting(get(7)),
		MapRaw:   formatted(get(7)),
		Gamemode: get(8),
	}
	online, _ := strconv.Atoi(get(4))
	slots, _ := strconv.Atoi(get(5))
	info.PlayersOnline, info.PlayersMax = count(online), count(slots)
	// The ports the server is configured for, as it announces them for LAN
	// discovery. Behind port forwarding they differ from the probed port.
	info.Port4 = announcedPort(get(10))
	info.Port6 = announcedPort(get(11))
	if info.Edition == "" {
		return nil, probeError("empty pong payload")
	}
	return info, nil
}

// announcedPort is the port in s, 0 when s is no valid port.
func announcedPort(s string) int {
	p, err := strconv.Atoi(s)
	if err != nil || p < 1 || p > 65535 {
		return 0
	}
	return p
}
