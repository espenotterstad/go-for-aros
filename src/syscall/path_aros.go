// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package syscall

// Go code on AROS sees Go paths (go/HANDOFF.md D15): '/' separates, "." and
// ".." are the current and parent directory, "Work:" is a volume (an
// absolute path), and a leading '/' is the root of the current volume.
// arosPath translates one to AmigaDOS before a dos call, goPath a name dos
// returns back. Both use only Errno values, so a host test can run them.
// They clean as path/filepath.Clean does; syscall can't import it (io/fs
// imports time, which imports syscall).

// arosVolumeLen returns the length of path's volume name, up to and
// including the first ':' when no '/' comes before it, else 0.
func arosVolumeLen(path string) int {
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

// arosPath translates Go path p to AmigaDOS:
//
//	x/./y   x/y          ../x     /x (each leading ".." one '/')
//	.       "" (current) /x       :x (root of the current volume)
//	Work:x  Work:x       Work:/x  Work:x
//
// It cleans first. A ".." that would climb above a volume ("Work:..") and a
// ':' outside the volume name are EINVAL; "" is ENOENT (dos would read it
// as the current directory).
func arosPath(p string) (string, error) {
	if p == "" {
		return "", ENOENT
	}
	vol := arosVolumeLen(p)
	rest := p[vol:]
	rooted := len(rest) > 0 && rest[0] == '/'
	var elems []string
	up := 0 // leading ".." elements of a relative path
	for i := 0; i < len(rest); {
		j := i
		for j < len(rest) && rest[j] != '/' {
			j++
		}
		e := rest[i:j]
		i = j + 1
		switch e {
		case "", ".":
		case "..":
			switch {
			case len(elems) > 0:
				elems = elems[:len(elems)-1]
			case rooted:
				// the root's parent is the root, as in Clean
			case vol > 0:
				return "", EINVAL
			default:
				up++
			}
		default:
			for k := 0; k < len(e); k++ {
				if e[k] == ':' || e[k] == 0 {
					return "", EINVAL
				}
			}
			elems = append(elems, e)
		}
	}
	n := vol + up
	if rooted && vol == 0 {
		n++
	}
	for _, e := range elems {
		n += len(e) + 1
	}
	b := make([]byte, 0, n)
	b = append(b, p[:vol]...)
	if rooted && vol == 0 {
		b = append(b, ':')
	}
	for range up {
		b = append(b, '/')
	}
	for i, e := range elems {
		if i > 0 {
			b = append(b, '/')
		}
		b = append(b, e...)
	}
	return string(b), nil
}

// goPath translates AmigaDOS path p, as dos returns it (NameFromLock,
// ReadLink, GetProgramName), to a Go path: a '/' where a name would start
// is the parent ("/x" is "../x", "a//b" is "a/../b", "Work:a//b" is
// "Work:a/../b"), and ":x" is "/x". A parent above a volume's root
// ("Work:/x") is EINVAL.
func goPath(p string) (string, error) {
	vol := arosVolumeLen(p)
	b := make([]byte, 0, len(p)+8)
	rest := p[vol:]
	if vol == 1 { // ":x", the root of the current volume
		b = append(b, '/')
	} else {
		b = append(b, p[:vol]...)
	}
	first := true
	depth := 0 // names below the volume's root
	for i := 0; i < len(rest); {
		if !first {
			b = append(b, '/')
		}
		first = false
		if rest[i] == '/' {
			if vol > 0 {
				if depth == 0 {
					return "", EINVAL
				}
				depth--
			}
			b = append(b, ".."...)
			i++
			continue
		}
		j := i
		for j < len(rest) && rest[j] != '/' {
			j++
		}
		b = append(b, rest[i:j]...)
		depth++
		i = j + 1 // skips the separator
	}
	return string(b), nil
}
