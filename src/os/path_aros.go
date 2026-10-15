// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package os

const (
	PathSeparator     = '/' // OS-specific path separator
	PathListSeparator = ';' // OS-specific path list separator (':' ends a volume name)
)

// IsPathSeparator reports whether c is a directory separator character.
// The ':' after a volume name is not one.
func IsPathSeparator(c uint8) bool {
	return PathSeparator == c
}

// basename returns name's last element for FileInfo.Name: what follows
// the last '/' or the volume's ':', trailing slashes removed. A name with
// nothing there ("Work:", "/", ".") gives dosName, the name dos reports.
func basename(name, dosName string) string {
	for len(name) > 1 && name[len(name)-1] == '/' {
		name = name[:len(name)-1]
	}
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '/' || name[i] == ':' {
			name = name[i+1:]
			break
		}
	}
	if name == "" || name == "/" || name == "." || name == ".." {
		return dosName
	}
	return name
}
