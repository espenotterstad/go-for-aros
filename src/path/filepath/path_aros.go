// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package filepath

import (
	"os"
	"strings"
)

// Paths on AROS (go/HANDOFF.md D15) are shaped like Windows' with '/' as
// the only separator: "Work:" is a volume name, a path with one is
// absolute, a leading '/' is the current volume's root. dos names ignore
// case.

// HasPrefix exists for historical compatibility and should not be used.
//
// Deprecated: HasPrefix does not respect path boundaries and
// does not ignore case when required.
func HasPrefix(p, prefix string) bool {
	if strings.HasPrefix(p, prefix) {
		return true
	}
	return strings.HasPrefix(strings.ToLower(p), strings.ToLower(prefix))
}

func splitList(path string) []string {
	if path == "" {
		return []string{}
	}
	return strings.Split(path, string(ListSeparator))
}

// abs joins a relative path to the current directory, and roots a path
// with a leading '/' on the current directory's volume: "/x" in "Work:a"
// is "Work:x".
func abs(path string) (string, error) {
	if IsAbs(path) {
		return Clean(path), nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(path, "/") {
		return join([]string{VolumeName(wd), strings.TrimLeft(path, "/")}), nil
	}
	return join([]string{wd, path}), nil
}

// join puts no separator after a volume name: "Work:" and "a" join to
// "Work:a", as "C:" and "a" do on Windows.
func join(elem []string) string {
	var b strings.Builder
	var lastChar byte
	for _, e := range elem {
		switch {
		case b.Len() == 0:
			// Add the first non-empty path element unchanged.
		case os.IsPathSeparator(lastChar):
			// If the path ends in a slash, strip any leading slashes from the next
			// path element to avoid creating a rooted path out of "/" + "/x".
			for len(e) > 0 && os.IsPathSeparator(e[0]) {
				e = e[1:]
			}
		case lastChar == ':':
			// If the path ends in a colon, keep the path relative to the volume's
			// root: "Work:" + "a" is "Work:a".
			for len(e) > 0 && os.IsPathSeparator(e[0]) {
				e = e[1:]
			}
		default:
			// In all other cases, add a separator between elements.
			b.WriteByte('/')
			lastChar = '/'
		}
		if len(e) > 0 {
			b.WriteString(e)
			lastChar = e[len(e)-1]
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return Clean(b.String())
}

func sameWord(a, b string) bool {
	return strings.EqualFold(a, b)
}
