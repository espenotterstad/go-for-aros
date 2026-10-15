// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package filepath

import (
	"os"
	"strings"
	"syscall"
)

// evalSymlinks checks that path exists and cleans it: package os reads no
// soft links on AROS yet (dos follows them itself), as on Plan 9.
func evalSymlinks(path string) (string, error) {
	if len(path) > 0 {
		// Check validity of path
		_, err := os.Lstat(path)
		if err != nil {
			// Return the same error value as on other operating systems.
			if strings.Contains(err.Error(), "not a directory") {
				err = syscall.ENOTDIR
			}
			return "", err
		}
	}
	return Clean(path), nil
}
