// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package exec

import (
	"errors"
)

// ErrNotFound is the error resulting if a path search failed to find an executable file.
var ErrNotFound = errors.New("executable file not found in $PATH")

// lookPath finds nothing: AROS starts no child processes yet
// (os.StartProcess fails), as on wasm.
func lookPath(file string) (string, error) {
	return "", &Error{file, ErrNotFound}
}

func lookExtensions(path, dir string) (string, error) {
	return path, nil
}
