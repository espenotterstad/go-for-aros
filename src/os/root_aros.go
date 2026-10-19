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
// and escapes, as a leading '/' does. ".." in the name is lexical, as on
// Windows: syscall cleans a name before dos sees it (go/HANDOFF.md D15).
// ".." in a link's target is not: dos follows the target itself, a name
// at a time, so the walk does too (a link to "s/../f" where s is a link
// leads where s leads, then up).
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

// splitNameInRoot splits a name given to a Root into its components: a
// volume name escapes, and the name is cleaned first, as syscall cleans it,
// so a ".." above the root escapes.
func splitNameInRoot(name string) ([]string, bool, error) {
	if filepathlite.VolumeNameLen(name) > 0 {
		return nil, false, errPathEscapes
	}
	if name == "" || IsPathSeparator(name[0]) {
		return splitPathInRoot(name, nil, nil) // "empty path", errPathEscapes
	}
	// "a/." must name a directory, as "a/" (hasTrailingSlash): the walk
	// follows a link there, as Lstat will.
	endsInSlash := IsPathSeparator(name[len(name)-1]) || hasTrailingSlash(name)
	name = filepathlite.Clean(name)
	if name == ".." || stringslite.HasPrefix(name, "../") {
		return nil, false, errPathEscapes
	}
	parts, _, err := splitPathInRoot(name, nil, nil)
	return parts, endsInSlash, err
}

func checkPathEscapesInternal(r *Root, name string, lstat bool) error {
	if r.root.closed.Load() {
		return ErrClosed
	}
	parts, endsInSlash, err := splitNameInRoot(name)
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
			if filepathlite.VolumeNameLen(link) > 0 {
				return errPathEscapes
			}
			newparts, newEndsInSlash, err := splitPathInRoot(link, parts[:i], parts[i+1:])
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
