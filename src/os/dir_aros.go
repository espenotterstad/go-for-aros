// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package os

import (
	"io"
	"io/fs"
	"syscall"
)

// readdir reads the directory with Examine and ExNext on the lock the
// File holds. Entries named "." or ".." (ram-handler accepts such names)
// are skipped, as on Unix. The read lock keeps Close from unlocking the
// lock while it is read.
func (file *File) readdir(n int, mode readdirMode) (names []string, dirents []DirEntry, infos []FileInfo, err error) {
	if err := file.readLock(); err != nil {
		return nil, nil, nil, &PathError{Op: "readdir", Path: file.name, Err: err}
	}
	defer file.readUnlock()
	if !file.isDir {
		return nil, nil, nil, &PathError{Op: "readdir", Path: file.name, Err: syscall.ENOTDIR}
	}
	var d *dirInfo
	for {
		d = file.dirinfo.Load()
		if d != nil {
			break
		}
		newD := new(dirInfo)
		if file.dirinfo.CompareAndSwap(nil, newD) {
			d = newD
			break
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.started {
		if err := syscall.Examine(file.sysfd, &d.fib); err != nil {
			return nil, nil, nil, &PathError{Op: "readdir", Path: file.name, Err: err}
		}
		path, err := syscall.NameFromLock(file.sysfd)
		if err != nil {
			return nil, nil, nil, &PathError{Op: "readdir", Path: file.name, Err: err}
		}
		if path != "" && path[len(path)-1] != ':' {
			path += "/"
		}
		d.path, d.started = path, true
	}

	if n <= 0 {
		n = -1
	}
	for n != 0 && !d.done {
		if err := syscall.ExNext(file.sysfd, &d.fib); err != nil {
			if err == syscall.ERROR_NO_MORE_ENTRIES {
				d.done = true
				break
			}
			return names, dirents, infos, &PathError{Op: "readdir", Path: file.name, Err: err}
		}
		name := d.fib.Name()
		if name == "." || name == ".." {
			continue
		}
		if mode == readdirName {
			names = append(names, name)
		} else {
			f := fileStatFromFIB(&d.fib, d.path+name)
			if mode == readdirDirEntry {
				dirents = append(dirents, dirEntry{f})
			} else {
				infos = append(infos, f)
			}
		}
		n--
	}
	if n > 0 && len(names)+len(dirents)+len(infos) == 0 {
		return nil, nil, nil, io.EOF
	}
	return names, dirents, infos, nil
}

type dirEntry struct {
	fs *fileStat
}

func (de dirEntry) Name() string            { return de.fs.Name() }
func (de dirEntry) IsDir() bool             { return de.fs.IsDir() }
func (de dirEntry) Type() FileMode          { return de.fs.Mode().Type() }
func (de dirEntry) Info() (FileInfo, error) { return de.fs, nil }

func (de dirEntry) String() string {
	return fs.FormatDirEntry(de)
}
