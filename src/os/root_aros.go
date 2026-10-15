// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package os

import (
	"internal/filepathlite"
)

// checkPathEscapes reports whether name escapes the root. AROS has no
// *at calls (root_noopenat.go): a name is checked lexically, as on Plan 9;
// filepathlite.IsLocal refuses a volume name and any ':'.
func checkPathEscapes(r *Root, name string) error {
	if r.root.closed.Load() {
		return ErrClosed
	}
	if !filepathlite.IsLocal(name) {
		return errPathEscapes
	}
	return nil
}

func checkPathEscapesLstat(r *Root, name string) error {
	return checkPathEscapes(r, name)
}
