// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package os

import (
	"internal/filepathlite"
	"syscall"
)

// executable returns the program's name as the shell started it: as given
// if absolute ("SYS:x/prog"), else its last element in the program's
// directory (PROGDIR:).
func executable() (string, error) {
	name, err := syscall.GetProgramName()
	if err != nil {
		return "", err
	}
	if filepathlite.IsAbs(name) {
		return name, nil
	}
	dir, err := syscall.NameFromLock(syscall.GetProgramDir())
	if err != nil {
		return "", err
	}
	if dir != "" && dir[len(dir)-1] != ':' {
		dir += "/"
	}
	return dir + basename(name, name), nil
}
