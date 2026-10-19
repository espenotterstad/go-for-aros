// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package os

import (
	"errors"
	"internal/filepathlite"
	"internal/stringslite"
	"slices"
	"syscall"
)

// AROS has no *at calls (root_noopenat.go). checkPathEscapes walks name
// one component at a time with Lstat and Readlink, as on js
// (root_js.go, go/HANDOFF.md D31), so a soft link can't lead out of the
// root. A name or link target with a volume name ("Work:x") is absolute
// and escapes, as a leading '/' does.
//
// Due to the lack of openat, checkPathEscapes is subject to TOCTOU races
// when symlinks change during the resolution process.
func checkPathEscapes(r *Root, name string) error {
	return checkPathEscapesInternal(r, name, false)
}

// checkPathEscapesLstat reports whether name escapes the root.
// It does not resolve symlinks in the final path component.
func checkPathEscapesLstat(r *Root, name string) error {
	return checkPathEscapesInternal(r, name, true)
}

// splitPathInRootVolume is splitPathInRoot for AROS: a volume name
// escapes, and ".." is lexical, as on Windows (rootCleanPath): syscall
// cleans a name before dos sees it (go/HANDOFF.md D15), so the walk cleans
// prefix, s and suffix together first, and a ".." above the root escapes.
func splitPathInRootVolume(s string, prefix, suffix []string) ([]string, bool, error) {
	if filepathlite.VolumeNameLen(s) > 0 {
		return nil, false, errPathEscapes
	}
	if s == "" || IsPathSeparator(s[0]) {
		return splitPathInRoot(s, prefix, suffix) // "empty path", errPathEscapes
	}
	endsInSlash := IsPathSeparator(s[len(s)-1])
	full := s
	for i := len(prefix) - 1; i >= 0; i-- {
		full = prefix[i] + "/" + full
	}
	for _, p := range suffix {
		full += "/" + p
	}
	full = filepathlite.Clean(full)
	if full == ".." || stringslite.HasPrefix(full, "../") {
		return nil, false, errPathEscapes
	}
	parts, _, err := splitPathInRoot(full, nil, nil)
	return parts, endsInSlash, err
}

func checkPathEscapesInternal(r *Root, name string, lstat bool) error {
	if r.root.closed.Load() {
		return ErrClosed
	}
	parts, endsInSlash, err := splitPathInRootVolume(name, nil, nil)
	if err != nil {
		return err
	}

	i := 0
	symlinks := 0
	base := r.root.name
	for i < len(parts) {
		if parts[i] == ".." {
			// Resolve one or more parent ("..") path components.
			end := i + 1
			for end < len(parts) && parts[end] == ".." {
				end++
			}
			count := end - i
			if count > i {
				return errPathEscapes
			}
			parts = slices.Delete(parts, i-count, end)
			i -= count
			base = r.root.name
			for j := range i {
				base = joinPath(base, parts[j])
			}
			continue
		}

		part := parts[i]
		if i == len(parts)-1 {
			if lstat && !endsInSlash {
				break
			}
		}

		next := joinPath(base, part)
		fi, err := Lstat(next)
		if err != nil {
			if IsNotExist(err) {
				return nil
			}
			return underlyingError(err)
		}
		if fi.Mode()&ModeSymlink != 0 {
			link, err := Readlink(next)
			if err != nil {
				return errPathEscapes
			}
			symlinks++
			if symlinks > rootMaxSymlinks {
				return errors.New("too many symlinks")
			}
			newparts, newEndsInSlash, err := splitPathInRootVolume(link, parts[:i], parts[i+1:])
			if err != nil {
				return err
			}
			if i == len(parts)-1 && newEndsInSlash {
				endsInSlash = true
			}
			parts = newparts
			continue
		}
		if !fi.IsDir() && i < len(parts)-1 {
			return syscall.ENOTDIR
		}

		base = next
		i++
	}
	return nil
}
