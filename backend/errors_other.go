// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !linux

package main

// noNetwork is always false: ENONET exists on Linux only.
func noNetwork(error) bool {
	return false
}
