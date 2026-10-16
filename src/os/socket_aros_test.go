// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package os_test

import "testing"

// tempDirWithUnixSocket skips: aros has no package net yet, so os's tests
// make no sockets (socket_test.go).
func tempDirWithUnixSocket(t *testing.T, name string) string {
	t.Skip("no unix sockets on aros: package net isn't ported yet")
	return ""
}
