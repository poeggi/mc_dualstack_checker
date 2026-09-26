// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"syscall"
)

// noNetwork reports ENONET, "machine is not on the network", an errno
// only Linux has.
func noNetwork(err error) bool {
	return errors.Is(err, syscall.ENONET)
}
