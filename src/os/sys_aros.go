// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package os

import "syscall"

// hostname is the HOSTNAME variable. AROS keeps the network stack's host
// name in bsdsocket.library, which Go doesn't use yet.
func hostname() (name string, err error) {
	if name, ok := syscall.Getenv("HOSTNAME"); ok && name != "" {
		return name, nil
	}
	return "", NewSyscallError("hostname", syscall.ENOSYS)
}
