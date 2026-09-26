// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"strings"
	"testing"
)

func TestConnectionID(t *testing.T) {
	for id, want := range map[string]bool{
		"a":                     true,
		"team a":                true,
		"x!~?&=@":               true,
		strings.Repeat("a", 64): true,
		"":                      false,
		" a":                    false,
		"a ":                    false,
		"a,b":                   false,
		strings.Repeat("a", 65): false,
		"a" + string(rune(9)):   false,
		string(rune(0xe4)):      false,
	} {
		if got := connectionID(id); got != want {
			t.Errorf("connectionID(%q) = %v, want %v", id, got, want)
		}
	}
}
