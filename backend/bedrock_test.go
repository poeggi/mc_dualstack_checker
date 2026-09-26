// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"encoding/binary"
	"testing"
)

func TestParsePongPorts(t *testing.T) {
	payload := "MCPE;\u00a7bHello;766;1.21.50;3;20;123;\u00a7aWorld;Survival;1;19132;19133;"
	b := []byte{raknetUnconnectedPong}
	b = binary.BigEndian.AppendUint64(b, 1)
	b = binary.BigEndian.AppendUint64(b, 2)
	b = append(b, raknetMagic...)
	b = binary.BigEndian.AppendUint16(b, uint16(len(payload)))
	b = append(b, payload...)
	info, err := parsePong(b)
	if err != nil {
		t.Fatal(err)
	}
	if info.Port4 != 19132 || info.Port6 != 19133 {
		t.Errorf("announced ports %d %d, want 19132 19133", info.Port4, info.Port6)
	}
	if info.ServerName != "Hello" || info.ServerNameRaw != "\u00a7bHello" || info.LevelRaw != "\u00a7aWorld" {
		t.Errorf("server name %q raw %q level raw %q", info.ServerName, info.ServerNameRaw, info.LevelRaw)
	}
}

func TestParsePongWeak(t *testing.T) {
	b := []byte{raknetUnconnectedPong}
	b = binary.BigEndian.AppendUint64(b, 1)
	b = binary.BigEndian.AppendUint64(b, 2)
	b = append(b, raknetMagic...)
	for name, pong := range map[string][]byte{
		"ends after the magic": b,
		"empty payload":        binary.BigEndian.AppendUint16(append([]byte{}, b...), 0),
	} {
		info, err := parsePong(pong)
		if err != nil || !info.weak {
			t.Errorf("%s: %+v %v, want a weak answer", name, info, err)
		}
	}
	if _, err := parsePong(b[:32]); err == nil {
		t.Errorf("a pong cut inside the magic was accepted")
	}
}
