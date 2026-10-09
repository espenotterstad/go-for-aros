// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package filepathlite

import (
	"internal/bytealg"
	"internal/stringslite"
)

// Paths on AROS (go/HANDOFF.md D15): '/' separates, "Work:" is a volume
// name, shaped like Windows' "C:", and a path with one is absolute. A
// leading '/' is the root of the current volume. Package syscall translates
// to AmigaDOS at every dos call.
const (
	Separator     = '/' // OS-specific path separator
	ListSeparator = ';' // OS-specific path list separator
)

func IsPathSeparator(c uint8) bool {
	return Separator == c
}

// isLocal also rejects a rooted path ("/x" is on the current volume's
// root) and any ':', which unixIsLocal doesn't see: Clean can turn
// "x/../Work:y" into the volume path "Work:y", and no dos name holds a ':'.
func isLocal(path string) bool {
	if stringslite.HasPrefix(path, "/") || bytealg.IndexByteString(path, ':') >= 0 {
		return false
	}
	return unixIsLocal(path)
}

// localize rejects ':' (it would make a volume name) and NUL.
func localize(path string) (string, error) {
	if bytealg.IndexByteString(path, ':') >= 0 || bytealg.IndexByteString(path, 0) >= 0 {
		return "", errInvalidPath
	}
	return path, nil
}

// IsAbs reports whether the path is absolute: whether it has a volume name.
func IsAbs(path string) bool {
	return volumeNameLen(path) > 0
}

// volumeNameLen returns the length of the leading volume name: up to and
// including the first ':', when no '/' comes before it.
func volumeNameLen(path string) int {
	for i := 0; i < len(path); i++ {
		switch path[i] {
		case ':':
			return i + 1
		case '/':
			return 0
		}
	}
	return 0
}
