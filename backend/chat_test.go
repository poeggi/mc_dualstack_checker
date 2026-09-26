// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestChatDepth(t *testing.T) {
	raw := `"x"`
	for i := 0; i < 1000; i++ {
		raw = `{"text":"a","extra":[` + raw + `]}`
	}
	if got := string(parseChat(json.RawMessage(raw), 0).visible()); got != strings.Repeat("a", maxChatDepth+1) {
		t.Errorf("parseChat kept %d levels, want %d", len(got), maxChatDepth+1)
	}
}

func TestLegacyChat(t *testing.T) {
	const s = "SECT"
	for _, c := range []struct{ name, raw, want string }{
		{"plain string keeps its codes", `"SECT6Gold"`, s + "6Gold"},
		{"named colour and bold", `{"text":"Hi","color":"gold","bold":true}`, s + "r" + s + "6" + s + "lHi"},
		{"hex colour", `{"text":"x","color":"#A1b2C3"}`, s + "r" + s + "x" + s + "a" + s + "1" + s + "b" + s + "2" + s + "c" + s + "3x"},
		{"children inherit, siblings reset", `{"text":"a","color":"red","extra":[{"text":"c","color":"blue"},"b",{"text":"d","bold":false}]}`,
			s + "r" + s + "ca" + s + "r" + s + "9c" + s + "r" + s + "cb" + s + "r" + s + "cd"},
		{"invalid colour is dropped", `{"text":"a","color":"#12"}`, s + "ra"},
	} {
		raw := strings.ReplaceAll(c.raw, s, "\u00a7")
		want := strings.ReplaceAll(c.want, s, "\u00a7")
		if got := string(parseChat(json.RawMessage(raw), 0).legacy()); got != want {
			t.Errorf("%s: %q, want %q", c.name, got, want)
		}
	}
}
