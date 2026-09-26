// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import "testing"

func TestClip(t *testing.T) {
	if got := clip("a\u00e4b", 2); got != "a" {
		t.Errorf("clip split a character: %q", got)
	}
	if got := clip("abc", 5); got != "abc" {
		t.Errorf("clip changed a short string: %q", got)
	}
}

func TestTaintedClean(t *testing.T) {
	r := func(n int) string { return string(rune(n)) }
	for _, c := range []struct {
		in, want string
		max      int
	}{
		{"  plain  ", "plain", 64},
		{"a" + r(0) + "b" + r(7) + "c" + r(0x7f) + "d" + r(0x9b) + "e", "abcde", 64},
		{"two\nlines\r", "two\nlines", 64},
		{"evil" + r(0x202e) + "txt.exe" + r(0x200b) + r(0xfeff), "eviltxt.exe", 64},
		{"bad\xff\xfeutf8", "badutf8", 64},
		{r(0xa7) + "6gold", r(0xa7) + "6gold", 64},
		{"a" + r(0xe4) + "b", "a", 2},
	} {
		if got := tainted(c.in).clean(c.max); got != c.want {
			t.Errorf("clean(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := tainted(r(0xa7) + "l" + r(0x202e) + "x").plain(64); got != "x" {
		t.Errorf("plain kept codes or format characters: %q", got)
	}
}
