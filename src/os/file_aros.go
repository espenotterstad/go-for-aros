// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package os

import (
	"internal/poll"
	"internal/testlog"
	"io"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// fixLongPath is a noop on non-Windows platforms.
func fixLongPath(path string) string {
	return path
}

// file is the real representation of *File.
// The extra level of indirection ensures that no clients of os
// can overwrite this data, which could cause the finalizer
// to close the wrong file descriptor.
type file struct {
	fdmu       poll.FDMutex
	sysfd      syscall.Handle // a dos file handle, or a lock if isDir
	isDir      bool           // dos opens no directory: os holds a lock on it
	borrowed   bool           // one of the shell's streams: never closed (go/HANDOFF.md D18)
	name       string
	dirinfo    atomic.Pointer[dirInfo] // nil unless directory being read
	appendMode bool                    // whether file is opened for appending
	rdonly     bool                    // opened O_RDONLY: Write gives EBADF (dos opens every file read-write)
	wronly     bool                    // opened O_WRONLY: Read gives EBADF
	farPos     int64                   // a position past the end that dos refused (seek), else 0
}

// fd is the AROS implementation of Fd: the dos handle, or the lock of a
// directory.
func (f *File) fd() uintptr {
	if f == nil {
		return ^(uintptr(0))
	}
	return uintptr(f.sysfd)
}

// newFileFromNewFile is called by [NewFile]. fd is a dos file handle; the
// shell's own streams are borrowed, so Close leaves them open.
func newFileFromNewFile(fd uintptr, name string) *File {
	if fd == ^uintptr(0) {
		return nil
	}
	h := syscall.Handle(fd)
	f := &File{&file{sysfd: h, name: name}}
	if h == syscall.Stdin || h == syscall.Stdout || h == syscall.Stderr {
		f.borrowed = true
		return f
	}
	runtime.SetFinalizer(f.file, (*file).close)
	return f
}

// newDirFile returns a File for a directory, holding lock.
func newDirFile(lock syscall.Handle, name string) *File {
	f := &File{&file{sysfd: lock, isDir: true, name: name}}
	runtime.SetFinalizer(f.file, (*file).close)
	return f
}

// Auxiliary information if the File describes a directory
type dirInfo struct {
	mu      sync.Mutex
	fib     syscall.FileInfoBlock // the last entry ExNext gave
	path    string                // the directory's full name, for SameFile
	started bool                  // Examine has run
	done    bool                  // ExNext ran out of entries
}

func epipecheck(file *File, e error) {
}

// DevNull is the name of the operating system's “null device.”
// On Unix-like systems, it is "/dev/null"; on Windows, "NUL";
// on AROS, "NIL:".
const DevNull = "NIL:"

// syscallMode returns the syscall-specific mode bits from Go's portable mode bits.
func syscallMode(i FileMode) (o uint32) {
	return uint32(i.Perm())
}

// hasTrailingSlash reports whether name ends in a slash or a "." name
// that dos would drop: "a/" and "a/.", not "/" or "Work:/". Either must
// name a directory, as on Unix.
func hasTrailingSlash(name string) bool {
	n := len(name)
	if n > 2 && name[n-1] == '.' && name[n-2] == '/' {
		return true
	}
	return n > 1 && name[n-1] == '/' && name[n-2] != ':'
}

// checkTrailingSlash refuses a name with a trailing slash that names no
// directory, before a dos call acts on the file dos would find without
// the slash: ENOTDIR for a file, the Lock error for a missing name, or
// EISDIR if it would be created (as Linux). nil for a directory and for a
// name without a trailing slash.
func checkTrailingSlash(name string, create bool) error {
	if !hasTrailingSlash(name) {
		return nil
	}
	lock, e := syscall.Lock(name, syscall.LockShared)
	if e != nil {
		if create && e == syscall.ERROR_OBJECT_NOT_FOUND {
			return syscall.EISDIR
		}
		return e
	}
	var fib syscall.FileInfoBlock
	e = syscall.Examine(lock, &fib)
	syscall.UnLock(lock)
	if e != nil {
		return e
	}
	if fib.DirEntryType <= 0 {
		return syscall.ENOTDIR
	}
	return nil
}

// openFileNolog is the AROS implementation of OpenFile.
func openFileNolog(name string, flag int, perm FileMode) (*File, error) {
	if e := checkTrailingSlash(name, flag&O_CREATE != 0); e != nil {
		return nil, &PathError{Op: "open", Path: name, Err: e}
	}
	fd, e := syscall.Open(name, flag, syscallMode(perm))
	if e == syscall.ERROR_OBJECT_WRONG_TYPE {
		// dos opens no directory (go/HANDOFF.md F24a): lock it, for
		// reading only, as Unix opens a directory.
		if flag&(O_WRONLY|O_RDWR|O_CREATE|O_TRUNC|O_APPEND) != 0 {
			return nil, &PathError{Op: "open", Path: name, Err: syscall.EISDIR}
		}
		return openDirNolog(name)
	}
	if e != nil {
		return nil, &PathError{Op: "open", Path: name, Err: e}
	}
	f := newFileFromNewFile(uintptr(fd), name)
	f.appendMode = flag&O_APPEND != 0
	f.rdonly = flag&(O_WRONLY|O_RDWR) == 0
	f.wronly = flag&O_WRONLY != 0
	return f, nil
}

func openDirNolog(name string) (*File, error) {
	lock, e := syscall.Lock(name, syscall.LockShared)
	if e != nil {
		return nil, &PathError{Op: "open", Path: name, Err: e}
	}
	var fib syscall.FileInfoBlock
	if e := syscall.Examine(lock, &fib); e != nil {
		syscall.UnLock(lock)
		return nil, &PathError{Op: "open", Path: name, Err: e}
	}
	if fib.DirEntryType <= 0 {
		syscall.UnLock(lock)
		return nil, &PathError{Op: "open", Path: name, Err: syscall.ENOTDIR}
	}
	return newDirFile(lock, name), nil
}

// Close closes the File, rendering it unusable for I/O.
// On files that support SetDeadline, any pending I/O operations will
// be canceled and return immediately with an ErrClosed error.
// Close will return an error if it has already been called.
func (f *File) Close() error {
	if f == nil {
		return ErrInvalid
	}
	return f.file.close()
}

func (file *file) close() error {
	if !file.fdmu.IncrefAndClose() {
		return &PathError{Op: "close", Path: file.name, Err: ErrClosed}
	}

	err := file.decref()

	// no need for a finalizer anymore
	runtime.SetFinalizer(file, nil)
	return err
}

// destroy actually closes the descriptor. This is called when
// there are no remaining references, by the decref, readUnlock,
// and writeUnlock methods.
func (file *file) destroy() error {
	switch {
	case file.borrowed:
		return nil
	case file.isDir:
		syscall.UnLock(file.sysfd)
		return nil
	}
	if e := syscall.Close(file.sysfd); e != nil {
		return &PathError{Op: "close", Path: file.name, Err: e}
	}
	return nil
}

// Stat returns the FileInfo structure describing file.
// If there is an error, it will be of type [*PathError].
func (f *File) Stat() (FileInfo, error) {
	if f == nil {
		return nil, ErrInvalid
	}
	if err := f.incref("stat"); err != nil {
		return nil, err
	}
	defer f.decref()
	var fs *fileStat
	var err error
	switch {
	case isNIL(f.name):
		return nilStat(f.name), nil
	case f.isDir:
		fs, err = fileStatFromLock(f.sysfd)
	default:
		fs, err = fileStatFromFH(f.sysfd)
	}
	if err != nil {
		return nil, &PathError{Op: "stat", Path: f.name, Err: err}
	}
	fs.name = basename(f.name, fs.name)
	return fs, nil
}

// Truncate changes the size of the file.
// It does not change the I/O offset.
// If there is an error, it will be of type [*PathError].
func (f *File) Truncate(size int64) error {
	if f == nil {
		return ErrInvalid
	}
	if err := f.incref("truncate"); err != nil {
		return err
	}
	defer f.decref()
	var err error
	switch {
	case f.isDir:
		err = syscall.EISDIR
	case f.rdonly:
		err = syscall.EINVAL // not open for writing, as ftruncate says
	default:
		err = f.truncate(size)
	}
	if err != nil {
		return &PathError{Op: "truncate", Path: f.name, Err: err}
	}
	return nil
}

// truncate sets the size, keeping the position: one past the new end
// (ram-handler moves it back to the end, go/HANDOFF.md F24b) becomes
// farPos, and a farPos the file now reaches is dos's position again.
func (f *File) truncate(size int64) error {
	cur := f.farPos
	if cur == 0 {
		var err error
		if cur, err = syscall.Seek(f.sysfd, 0, io.SeekCurrent); err != nil {
			return err
		}
	}
	if err := syscall.Ftruncate(f.sysfd, size); err != nil {
		return err
	}
	if cur > size {
		f.farPos = cur
		_, err := syscall.Seek(f.sysfd, 0, io.SeekEnd)
		return err
	}
	f.farPos = 0
	_, err := syscall.Seek(f.sysfd, cur, io.SeekStart)
	return err
}

// pastEnd reports whether farPos is still past the end. A farPos the file
// reaches now (WriteAt, another handle) is dos's position again.
func (f *File) pastEnd() bool {
	if f.farPos == 0 {
		return false
	}
	if _, err := syscall.Seek(f.sysfd, f.farPos, io.SeekStart); err != nil {
		return true // dos refused it and kept its position
	}
	f.farPos = 0
	return false
}

// growToFarPos makes the file reach farPos before a write there, as Unix
// does (dos fills the new bytes with zeros), and seeks to it.
func (f *File) growToFarPos() error {
	if f.farPos == 0 || f.appendMode || !f.pastEnd() {
		return nil
	}
	if err := syscall.Ftruncate(f.sysfd, f.farPos); err != nil {
		return err
	}
	if _, err := syscall.Seek(f.sysfd, f.farPos, io.SeekStart); err != nil {
		return err
	}
	f.farPos = 0
	return nil
}

func (f *File) chmod(mode FileMode) error {
	if f == nil {
		return ErrInvalid
	}
	if err := f.incref("chmod"); err != nil {
		return err
	}
	defer f.decref()
	name, err := f.fullName()
	if err == nil {
		err = syscall.Chmod(name, syscallMode(mode))
	}
	if err != nil {
		return &PathError{Op: "chmod", Path: f.name, Err: err}
	}
	return nil
}

// fullName returns f's full name, wherever the current directory is now.
func (f *File) fullName() (string, error) {
	if f.isDir {
		return syscall.NameFromLock(f.sysfd)
	}
	return syscall.NameFromFH(f.sysfd)
}

// Sync commits the current contents of the file to stable storage. dos
// writes through unbuffered handles, so there is nothing to flush.
func (f *File) Sync() error {
	return f.checkValid("sync")
}

// read reads up to len(b) bytes from the File.
// It returns the number of bytes read and an error, if any.
func (f *File) read(b []byte) (n int, err error) {
	if err := f.readLock(); err != nil {
		return 0, err
	}
	defer f.readUnlock()
	if f.isDir {
		return 0, syscall.EISDIR
	}
	if f.wronly {
		return 0, syscall.EBADF
	}
	if f.pastEnd() {
		return 0, io.EOF
	}
	n, e := syscall.Read(f.sysfd, b)
	if n == 0 && len(b) > 0 && e == nil {
		return 0, io.EOF
	}
	return n, e
}

// pread reads len(b) bytes from the File starting at byte offset off.
// It returns the number of bytes read and the error, if any.
// EOF is signaled by a zero count with err set to nil.
// dos has no positioned read: pread seeks there and back, holding the
// file's read and write locks (not atomic against other handles).
func (f *File) pread(b []byte, off int64) (n int, err error) {
	if err := f.readLock(); err != nil {
		return 0, err
	}
	defer f.readUnlock()
	if err := f.writeLock(); err != nil {
		return 0, err
	}
	defer f.writeUnlock()
	if f.isDir {
		return 0, syscall.EISDIR
	}
	if f.wronly {
		return 0, syscall.EBADF
	}
	err = f.at(off, func() error {
		n, err = syscall.Read(f.sysfd, b)
		return err
	})
	if err == syscall.ERROR_SEEK_ERROR || n == 0 && len(b) > 0 && err == nil {
		return 0, io.EOF // at or past the end
	}
	return n, err
}

// at runs fn with f's position at off, then puts the position back.
func (f *File) at(off int64, fn func() error) error {
	cur, err := syscall.Seek(f.sysfd, 0, io.SeekCurrent)
	if err != nil {
		return err
	}
	if _, err := syscall.Seek(f.sysfd, off, io.SeekStart); err != nil {
		return err
	}
	err = fn()
	if _, e := syscall.Seek(f.sysfd, cur, io.SeekStart); err == nil {
		err = e
	}
	return err
}

// write writes len(b) bytes to the File.
// It returns the number of bytes written and an error, if any.
func (f *File) write(b []byte) (n int, err error) {
	if err := f.writeLock(); err != nil {
		return 0, err
	}
	defer f.writeUnlock()
	if f.isDir {
		return 0, syscall.EISDIR
	}
	if f.rdonly {
		return 0, syscall.EBADF
	}
	if err := f.growToFarPos(); err != nil {
		return 0, err
	}
	for n < len(b) {
		m, err := syscall.Write(f.sysfd, b[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// pwrite writes len(b) bytes to the File starting at byte offset off.
// It returns the number of bytes written and an error, if any. Like pread,
// it seeks there and back; dos refuses an offset past the end.
func (f *File) pwrite(b []byte, off int64) (n int, err error) {
	if err := f.readLock(); err != nil {
		return 0, err
	}
	defer f.readUnlock()
	if err := f.writeLock(); err != nil {
		return 0, err
	}
	defer f.writeUnlock()
	if f.isDir {
		return 0, syscall.EISDIR
	}
	if f.rdonly {
		return 0, syscall.EBADF
	}
	err = f.at(off, func() error {
		for n < len(b) {
			m, err := syscall.Write(f.sysfd, b[n:])
			n += m
			if err != nil {
				return err
			}
		}
		return nil
	})
	return n, err
}

// seek sets the offset for the next Read or Write on file to offset, interpreted
// according to whence: 0 means relative to the origin of the file, 1 means
// relative to the current offset, and 2 means relative to the end.
// It returns the new offset and an error, if any. A directory can only go
// back to its start (to read it again).
func (f *File) seek(offset int64, whence int) (ret int64, err error) {
	if err := f.incref(""); err != nil {
		return 0, err
	}
	defer f.decref()
	// Free cached dirinfo, so we allocate a new one if we
	// access this file as a directory again. See #35767 and #37161.
	f.dirinfo.Store(nil)
	if f.isDir {
		if offset != 0 || whence != io.SeekStart {
			return 0, syscall.EINVAL
		}
		return 0, nil
	}
	if whence == io.SeekCurrent && f.farPos > 0 {
		offset, whence = f.farPos+offset, io.SeekStart
	}
	ret, err = syscall.Seek(f.sysfd, offset, whence)
	if err == nil {
		f.farPos = 0
		return ret, nil
	}
	return f.seekPastEnd(offset, whence, err)
}

// seekPastEnd handles a seek dos refused: a position past the end becomes
// farPos, with dos's position at the end, and the next write grows the file
// to it (growToFarPos); any other position gives err.
func (f *File) seekPastEnd(offset int64, whence int, err error) (int64, error) {
	cur, e := syscall.Seek(f.sysfd, 0, io.SeekCurrent)
	if e != nil {
		return 0, err
	}
	end, e := syscall.Seek(f.sysfd, 0, io.SeekEnd)
	if e != nil {
		return 0, err
	}
	pos := offset
	switch whence {
	case io.SeekCurrent:
		pos += cur
	case io.SeekEnd:
		pos += end
	}
	if pos <= end || pos != int64(int32(pos)) {
		syscall.Seek(f.sysfd, cur, io.SeekStart)
		return 0, err
	}
	f.farPos = pos
	return pos, nil
}

// Truncate changes the size of the named file.
// If the file is a symbolic link, it changes the size of the link's target.
// If there is an error, it will be of type [*PathError].
func Truncate(name string, size int64) error {
	err := checkTrailingSlash(name, false)
	var fd syscall.Handle
	if err == nil {
		fd, err = syscall.Open(name, O_RDWR, 0)
	}
	if err == syscall.ERROR_OBJECT_WRONG_TYPE {
		err = syscall.EISDIR
	}
	if err == nil {
		err = syscall.Ftruncate(fd, size)
		syscall.Close(fd)
	}
	if err != nil {
		return &PathError{Op: "truncate", Path: name, Err: err}
	}
	return nil
}

// Remove removes the named file or directory.
// If there is an error, it will be of type [*PathError].
func Remove(name string) error {
	e := checkTrailingSlash(name, false)
	if e == nil {
		e = syscall.Remove(name)
	}
	if e != nil {
		return &PathError{Op: "remove", Path: name, Err: e}
	}
	return nil
}

// rename gives dos Rename Unix's semantics, as far as it can: an existing
// newname file is deleted first (not atomic), and a directory newname is
// refused (EEXIST) as on Unix. Names of the same object differ only in
// case: dos Rename gets those (the hosted handler refuses them, F24).
func rename(oldname, newname string) error {
	if err := checkTrailingSlash(oldname, false); err != nil {
		return &LinkError{"rename", oldname, newname, err}
	}
	if hasTrailingSlash(newname) {
		if fi, err := Stat(oldname); err == nil && !fi.IsDir() {
			return &LinkError{"rename", oldname, newname, syscall.ENOTDIR}
		}
	}
	if fi, err := Lstat(newname); err == nil {
		ofi, err := Lstat(oldname)
		if err != nil {
			if pe, ok := err.(*PathError); ok {
				err = pe.Err
			}
			return &LinkError{"rename", oldname, newname, err}
		}
		switch {
		case SameFile(fi, ofi):
			if newname == oldname {
				if fi.IsDir() {
					return &LinkError{"rename", oldname, newname, syscall.EEXIST}
				}
				return nil
			}
		case fi.IsDir():
			return &LinkError{"rename", oldname, newname, syscall.EEXIST}
		case ofi.IsDir():
			return &LinkError{"rename", oldname, newname, syscall.ENOTDIR}
		default:
			if err := syscall.Remove(newname); err != nil {
				return &LinkError{"rename", oldname, newname, err}
			}
		}
	}
	if err := syscall.Rename(oldname, newname); err != nil {
		return &LinkError{"rename", oldname, newname, err}
	}
	return nil
}

// See docs in file.go:Chmod.
func chmod(name string, mode FileMode) error {
	e := checkTrailingSlash(name, false)
	if e == nil {
		e = syscall.Chmod(name, syscallMode(mode))
	}
	if e != nil {
		return &PathError{Op: "chmod", Path: name, Err: e}
	}
	return nil
}

// Chtimes changes the access and modification times of the named
// file, similar to the Unix utime() or utimes() functions.
// A zero time.Time value will leave the corresponding file time unchanged.
//
// The underlying filesystem may truncate or round the values to a
// less precise time unit. dos keeps one date per file, in 1/50 s: Chtimes
// sets it from mtime and ignores atime.
// If there is an error, it will be of type [*PathError].
func Chtimes(name string, atime time.Time, mtime time.Time) error {
	err := checkTrailingSlash(name, false)
	if err == nil && mtime.IsZero() {
		_, err = Stat(name)
		if pe, ok := err.(*PathError); ok {
			err = pe.Err
		}
	} else if err == nil {
		var ds syscall.DateStamp
		if ds, err = dateStamp(mtime); err == nil {
			err = syscall.SetFileDate(name, &ds)
		}
	}
	if err != nil {
		return &PathError{Op: "chtimes", Path: name, Err: err}
	}
	return nil
}

// Pipe returns a connected pair of Files; reads from r return bytes
// written to w. It returns the files and an error, if any.
// AROS has none yet: it returns an error that is [errors.ErrUnsupported].
func Pipe() (r *File, w *File, err error) {
	return nil, nil, NewSyscallError("pipe", syscall.ENOSYS)
}

// Not supported on AROS yet: hard links and owners.

// Link creates newname as a hard link to the oldname file.
// If there is an error, it will be of type *LinkError.
func Link(oldname, newname string) error {
	return &LinkError{"link", oldname, newname, syscall.ENOSYS}
}

// Symlink creates newname as a symbolic link to oldname.
// On Windows, a symlink to a non-existent oldname creates a file symlink;
// if oldname is later created as a directory the symlink will not work.
// On AROS it is a dos soft link (go/HANDOFF.md D33); on the hosted
// handler, a host symlink holding the AROS path. There dos resolves a
// link as the last name, but the host follows one in the middle of a path
// as a host path: a target with ".." ("/x" to dos) leads to the host
// root's x, and a Root on the hosted handler doesn't see it (AROS bug
// B33).
// If there is an error, it will be of type *LinkError.
func Symlink(oldname, newname string) error {
	e := checkTrailingSlash(newname, true)
	if e == nil {
		e = syscall.Symlink(oldname, newname)
	}
	if e != nil {
		return &LinkError{"symlink", oldname, newname, e}
	}
	return nil
}

// readlink reads a soft link through dos ReadLink (go/HANDOFF.md D31). A
// trailing slash follows the link, as on Unix: what it names is no link.
func readlink(name string) (string, error) {
	var err error
	if hasTrailingSlash(name) {
		var fi FileInfo
		if fi, err = Stat(name); err == nil {
			err = syscall.EINVAL
			if !fi.IsDir() {
				err = syscall.ENOTDIR
			}
		} else if pe, ok := err.(*PathError); ok {
			err = pe.Err
		}
		return "", &PathError{Op: "readlink", Path: name, Err: err}
	}
	t, err := syscall.Readlink(name)
	if err != nil {
		return "", &PathError{Op: "readlink", Path: name, Err: err}
	}
	return t, nil
}

// Chown changes the numeric uid and gid of the named file.
// If the file is a symbolic link, it changes the uid and gid of the link's target.
// A uid or gid of -1 means to not change that value.
// If there is an error, it will be of type [*PathError].
//
// On Windows, Plan 9 or AROS, Chown always returns an error.
func Chown(name string, uid, gid int) error {
	return &PathError{Op: "chown", Path: name, Err: syscall.ENOSYS}
}

// Lchown changes the numeric uid and gid of the named file.
// If the file is a symbolic link, it changes the uid and gid of the link itself.
// If there is an error, it will be of type [*PathError].
func Lchown(name string, uid, gid int) error {
	return &PathError{Op: "lchown", Path: name, Err: syscall.ENOSYS}
}

// Chown changes the numeric uid and gid of the named file.
// If there is an error, it will be of type [*PathError].
func (f *File) Chown(uid, gid int) error {
	if f == nil {
		return ErrInvalid
	}
	return &PathError{Op: "chown", Path: f.name, Err: syscall.ENOSYS}
}

// tempDir is $TMPDIR, else T: (RAM Disk:T on a standard AROS).
func tempDir() string {
	dir := Getenv("TMPDIR")
	if dir == "" {
		dir = "T:"
	}
	return dir
}

// Chdir changes the current working directory to the file,
// which must be a directory.
// If there is an error, it will be of type [*PathError].
func (f *File) Chdir() error {
	if err := f.incref("chdir"); err != nil {
		return err
	}
	defer f.decref()
	err := error(syscall.ENOTDIR)
	if f.isDir {
		err = syscall.Fchdir(f.sysfd)
	}
	if err != nil {
		return &PathError{Op: "chdir", Path: f.name, Err: err}
	}
	if log := testlog.Logger(); log != nil {
		wd, err := Getwd()
		if err == nil {
			log.Chdir(wd)
		}
	}
	return nil
}

// setDeadline sets the read and write deadline.
func (f *File) setDeadline(time.Time) error {
	if err := f.checkValid("SetDeadline"); err != nil {
		return err
	}
	return poll.ErrNoDeadline
}

// setReadDeadline sets the read deadline.
func (f *File) setReadDeadline(time.Time) error {
	if err := f.checkValid("SetReadDeadline"); err != nil {
		return err
	}
	return poll.ErrNoDeadline
}

// setWriteDeadline sets the write deadline.
func (f *File) setWriteDeadline(time.Time) error {
	if err := f.checkValid("SetWriteDeadline"); err != nil {
		return err
	}
	return poll.ErrNoDeadline
}

// checkValid checks whether f is valid for use, but does not prepare
// to actually use it. If f is not ready checkValid returns an appropriate
// error, perhaps incorporating the operation name op.
func (f *File) checkValid(op string) error {
	if f == nil {
		return ErrInvalid
	}
	if err := f.incref(op); err != nil {
		return err
	}
	return f.decref()
}

type rawConn struct{}

func (c *rawConn) Control(f func(uintptr)) error {
	return syscall.ENOSYS
}

func (c *rawConn) Read(f func(uintptr) bool) error {
	return syscall.ENOSYS
}

func (c *rawConn) Write(f func(uintptr) bool) error {
	return syscall.ENOSYS
}

func newRawConn(file *File) (*rawConn, error) {
	return nil, syscall.ENOSYS
}

func ignoringEINTR(fn func() error) error {
	return fn()
}

func ignoringEINTR2[T any](fn func() (T, error)) (T, error) {
	return fn()
}
