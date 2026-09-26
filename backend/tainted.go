// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"strings"
	"unicode"
)

// tainted is text as a server sent it: any bytes, any length. Go has no
// taint mode like Perl's; a type of its own does the job. Go does not turn
// a tainted value into a string by itself, so server text reaches
// ServerInfo only through the methods below, which make it safe to pass on.
type tainted string

// clean is t made safe, cut to at most max bytes: valid UTF-8, without
// control characters other than the line break, without format characters
// such as bidi overrides and zero-width spaces, trimmed.
func (t tainted) clean(max int) string {
	s := strings.Map(func(r rune) rune {
		if r != '\n' && (unicode.IsControl(r) || unicode.Is(unicode.Cf, r)) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(string(t), ""))
	return clip(strings.TrimSpace(s), max)
}

// plain is clean text without formatting codes.
func (t tainted) plain(max int) string { return clip(stripFormatting(t.clean(len(t))), max) }

// coded is clean text with its formatting codes, "" when it has none.
func (t tainted) coded(max int) string { return formatted(t.clean(max)) }

// Longest server text kept. Status pages show a two-line MOTD; the other
// fields are short names and numbers. Colour codes can take more room than
// the text they colour. The limits keep cached results small.
const (
	motdMax  = 256
	rawMax   = 2048
	fieldMax = 64
)

// clip cuts s to at most n bytes without splitting a character.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}

// formatted returns s when it carries colour codes, else "".
func formatted(s string) string {
	if strings.ContainsRune(s, 0xA7) {
		return s
	}
	return ""
}

// stripFormatting removes Minecraft "section sign" colour codes.
func stripFormatting(s string) string {
	var out strings.Builder
	skip := false
	for _, r := range s {
		if skip {
			skip = false
			continue
		}
		if r == 0xA7 {
			skip = true
			continue
		}
		out.WriteRune(r)
	}
	return strings.TrimSpace(out.String())
}
