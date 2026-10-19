// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package os

import (
	"syscall"
	"time"
)

// arosLocalOffset is AROS's zone offset in seconds east of UTC, from the
// runtime (go/HANDOFF.md D21): a DateStamp is on AROS's clock, which is
// local time.
func arosLocalOffset() int

// dosEpoch is 1978-01-01, the day dos dates count from, in Unix seconds.
const dosEpoch = 2922 * 86400

// dateTime returns ds as a time.
func dateTime(ds syscall.DateStamp) time.Time {
	sec := (int64(ds.Days)*1440+int64(ds.Minute))*60 + int64(ds.Tick/syscall.TICKS_PER_SECOND) +
		dosEpoch - int64(arosLocalOffset())
	nsec := int64(ds.Tick%syscall.TICKS_PER_SECOND) * (1e9 / syscall.TICKS_PER_SECOND)
	return time.Unix(sec, nsec)
}

// dateStamp returns t as a DateStamp, rounded down to a tick; dos can't
// hold a date before 1978 (local time).
func dateStamp(t time.Time) (syscall.DateStamp, error) {
	sec := t.Unix() + int64(arosLocalOffset()) - dosEpoch
	if sec < 0 || sec/86400 > 1<<31-1 {
		return syscall.DateStamp{}, syscall.EINVAL
	}
	return syscall.DateStamp{
		Days:   int32(sec / 86400),
		Minute: int32(sec % 86400 / 60),
		Tick:   int32(sec%60*syscall.TICKS_PER_SECOND) + int32(t.Nanosecond()/(1e9/syscall.TICKS_PER_SECOND)),
	}, nil
}

// fileStatFromFIB fills a fileStat from fib; path is the object's full
// name.
func fileStatFromFIB(fib *syscall.FileInfoBlock, path string) *fileStat {
	fs := &fileStat{
		name:    fib.Name(),
		size:    int64(fib.Size),
		mode:    FileMode(fib.Perm()),
		modTime: dateTime(fib.Date),
		sys:     *fib,
		path:    path,
	}
	switch t := fib.DirEntryType; {
	case t == syscall.ST_SOFTLINK:
		fs.mode |= ModeSymlink
	case t > 0: // ST_ROOT, ST_USERDIR, ST_LINKDIR
		fs.mode |= ModeDir
	case t == syscall.ST_PIPEFILE:
		fs.mode |= ModeNamedPipe
	}
	return fs
}

func fileStatFromLock(lock syscall.Handle) (*fileStat, error) {
	var fib syscall.FileInfoBlock
	if err := syscall.Examine(lock, &fib); err != nil {
		return nil, err
	}
	path, err := syscall.NameFromLock(lock)
	if err != nil {
		return nil, err
	}
	return fileStatFromFIB(&fib, path), nil
}

func fileStatFromFH(fd syscall.Handle) (*fileStat, error) {
	var fib syscall.FileInfoBlock
	if err := syscall.ExamineFH(fd, &fib); err != nil {
		return nil, err
	}
	path, err := syscall.NameFromFH(fd)
	if err != nil {
		return nil, err
	}
	return fileStatFromFIB(&fib, path), nil
}

// isNIL reports whether name is NIL:, dos's null device, which has no
// handler to Lock or Examine.
func isNIL(name string) bool {
	return len(name) == 4 && name[3] == ':' && name[0]|0x20 == 'n' && name[1]|0x20 == 'i' && name[2]|0x20 == 'l'
}

// nilStat describes NIL: as Unix describes /dev/null: a character device.
func nilStat(name string) *fileStat {
	return &fileStat{name: name, mode: ModeDevice | ModeCharDevice | 0666, path: DevNull}
}

// statNolog implements Stat for AROS. dos follows soft links in Lock.
func statNolog(name string) (FileInfo, error) {
	if isNIL(name) {
		return nilStat(name), nil
	}
	lock, err := syscall.Lock(name, syscall.LockShared)
	if err != nil {
		return nil, &PathError{Op: "stat", Path: name, Err: err}
	}
	fs, err := fileStatFromLock(lock)
	syscall.UnLock(lock)
	if err != nil {
		return nil, &PathError{Op: "stat", Path: name, Err: err}
	}
	if hasTrailingSlash(name) && !fs.IsDir() {
		return nil, &PathError{Op: "stat", Path: name, Err: syscall.ENOTDIR}
	}
	fs.name = basename(name, fs.name)
	return fs, nil
}

// lstatNolog implements Lstat for AROS (go/HANDOFF.md D31): a soft link is
// described by its entry in the parent directory, with its target's length
// as its size, as on Unix; anything else as Stat. A trailing slash follows
// the link.
func lstatNolog(name string) (FileInfo, error) {
	if !isNIL(name) && !hasTrailingSlash(name) {
		var fib syscall.FileInfoBlock
		target, dir, err := syscall.LstatLink(name, &fib)
		if err == nil {
			fs := fileStatFromFIB(&fib, joinDosName(dir, fib.Name()))
			fs.mode = fs.mode&ModePerm | ModeSymlink
			fs.size = int64(len(target))
			fs.name = basename(name, fs.name)
			return fs, nil
		}
		if err != syscall.EINVAL {
			return nil, &PathError{Op: "lstat", Path: name, Err: err}
		}
	}
	fi, err := statNolog(name)
	if pe, ok := err.(*PathError); ok {
		pe.Op = "lstat"
	}
	return fi, err
}

// joinDosName joins a directory's full name and a name in it: "Work:" and
// "x" give "Work:x", "Work:a" and "x" "Work:a/x".
func joinDosName(dir, name string) string {
	if dir == "" || dir[len(dir)-1] == ':' || dir[len(dir)-1] == '/' {
		return dir + name
	}
	return dir + "/" + name
}

// For testing.
func atime(fi FileInfo) time.Time {
	return fi.ModTime()
}
