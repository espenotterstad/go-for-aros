// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package signal

import (
	"os"
	"syscall"
)

// AROS delivers no signals to Go yet: Notify registers a channel that
// never receives, and Ignore changes nothing.

const numSig = 65 // as on Unix: room for every syscall.Signal os names

func signum(sig os.Signal) int {
	switch sig := sig.(type) {
	case syscall.Signal:
		i := int(sig)
		if i < 0 || i >= numSig {
			return -1
		}
		return i
	default:
		return -1
	}
}

func enableSignal(sig int)  {}
func disableSignal(sig int) {}
func ignoreSignal(sig int)  {}

func signalIgnored(sig int) bool {
	return false
}
