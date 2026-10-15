// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sysrand

import "errors"

// AROS has no entropy source Go can use yet (go/HANDOFF.md D16): the first
// use of crypto randomness fails.
func read(b []byte) error {
	return errors.New("aros: no entropy source")
}
