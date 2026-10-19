// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// AROS system calls: dos.library through the runtime's aroscall, which runs
// every AROS call on g0 (go/HANDOFF.md D3, D11). A Handle is a dos BPTR, a
// file handle or a lock. Paths are Go paths; arosPath translates them
// before every dos call (path_aros.go, D15).

package syscall

import (
	"sync"
	"unsafe"
)

const ImplementsGetwd = true

// A Handle is a dos file handle or lock (a BPTR; a plain pointer on 64-bit
// AROS).
type Handle uintptr

// Implemented in the runtime (runtime/os_aros.go). aroscall makes the call
// as a system call and reads IoErr in the same one, so no other goroutine's
// dos call can come between.
func aroscall(fn, a0, a1, a2, a3, a4, a5 uintptr) (r, ioerr uintptr)
func arosBases() (sysBase, dosBase uintptr)
func arosShellDir() uintptr

var sysBase, dosBase = arosBases()

// dosCall calls dos.library function lvo. The caller passes dosBase right
// after the function's own arguments (the aarch64 AROS library ABI). It
// returns the raw result, whose width is the C function's (LONG results
// are int32, BOOL int16), and IoErr.
//
//go:nosplit
//go:uintptrkeepalive
func dosCall(lvo, a0, a1, a2, a3, a4 uintptr) (uintptr, Errno) {
	r, e := aroscall(*(*uintptr)(unsafe.Pointer(dosBase - 8*lvo)), a0, a1, a2, a3, a4, 0)
	return r, Errno(e)
}

// iptr passes a negative dos constant (SHARED_LOCK, OFFSET_BEGINNING) as a
// register value; a constant conversion to uintptr would not compile.
func iptr(v int) uintptr { return uintptr(v) }

// dosPath translates a Go path to AmigaDOS and returns it NUL-terminated.
func dosPath(path string) (*byte, error) {
	p, err := arosPath(path)
	if err != nil {
		return nil, err
	}
	return BytePtrFromString(p)
}

// The process's standard streams, borrowed from the shell (go/HANDOFF.md
// D18): Input(), Output(), and pr_CES (Output() when the shell gives none).
// Closing them is the borrower's mistake; package os never does.
var (
	Stdin  = stdHandle(_LVO_Input)
	Stdout = stdHandle(_LVO_Output)
	Stderr = stderrHandle()
)

// findTask returns the calling task (exec FindTask(NULL)): a Process.
func findTask() uintptr {
	t, _ := aroscall(*(*uintptr)(unsafe.Pointer(sysBase - 8*_LVO_FindTask)), 0, sysBase, 0, 0, 0, 0)
	return t
}

func stdHandle(lvo uintptr) Handle {
	r, _ := dosCall(lvo, dosBase, 0, 0, 0, 0)
	return Handle(r)
}

func stderrHandle() Handle {
	if h := *(*Handle)(unsafe.Pointer(findTask() + _Process_pr_CES)); h != 0 {
		return h
	}
	return Stdout
}

// Timespec and Timeval are what the shared syscall.go expects; dos itself
// uses DateStamp.
type Timespec struct {
	Sec  int64
	Nsec int64
}

type Timeval struct {
	Sec  int64
	Usec int64
}

// Flags for Open. dos has three open modes; Open maps these onto them.
const (
	O_RDONLY  = 0x0
	O_WRONLY  = 0x1
	O_RDWR    = 0x2
	O_CREAT   = 0x40
	O_EXCL    = 0x80
	O_TRUNC   = 0x200
	O_APPEND  = 0x400
	O_SYNC    = 0x1000
	O_CLOEXEC = 0
)

// dos has no append mode: Write seeks to the end first on a handle opened
// with O_APPEND (not atomic with the write).
// pendingProt holds the protection of a file created without owner write
// until Close (Open). Both maps are guarded by appendLock.
var (
	appendLock  sync.Mutex
	appendFDs   map[Handle]bool
	pendingProt map[Handle]uint32
)

func isAppend(fd Handle) bool {
	appendLock.Lock()
	defer appendLock.Unlock()
	return appendFDs[fd]
}

// Open opens path. O_CREAT checks with Lock first whether the file exists
// (not atomic): O_EXCL fails if it does, and a file Open creates gets perm
// (permProt; no umask). Every create is MODE_READWRITE (open or create, a
// shared lock), O_TRUNC a SetFileSize after it: MODE_NEWFILE would lock the
// file exclusively while it is open and, on ram-handler, delete and
// recreate an existing one. A handler without SetFileSize (NIL:) ignores
// O_TRUNC.
func Open(path string, mode int, perm uint32) (fd Handle, err error) {
	p, err := dosPath(path)
	if err != nil {
		return 0, err
	}
	dmode := uintptr(_MODE_OLDFILE) // existing file, read and write
	create := false
	if mode&O_CREAT != 0 {
		if l, _ := dosCall(_LVO_Lock, uintptr(unsafe.Pointer(p)), iptr(_SHARED_LOCK), dosBase, 0, 0); l != 0 {
			dosCall(_LVO_UnLock, l, dosBase, 0, 0, 0)
			if mode&O_EXCL != 0 {
				return 0, EEXIST
			}
		} else {
			create = true
		}
		dmode = _MODE_READWRITE
	}
	r, e := dosCall(_LVO_Open, uintptr(unsafe.Pointer(p)), dmode, dosBase, 0, 0)
	if r == 0 {
		return 0, notFound(path, e)
	}
	fd = Handle(r)
	if create && perm&0200 == 0 {
		// ram-handler checks FIBF_WRITE at every Write: a file created
		// without owner write gets its protection at Close, so the handle
		// that created it can write, as on Unix.
		appendLock.Lock()
		if pendingProt == nil {
			pendingProt = make(map[Handle]uint32)
		}
		pendingProt[fd] = permProt(perm)
		appendLock.Unlock()
	} else if create {
		dosCall(_LVO_SetProtection, uintptr(unsafe.Pointer(p)), uintptr(permProt(perm)), dosBase, 0, 0)
	}
	if mode&O_TRUNC != 0 {
		r, e := dosCall(_LVO_SetFileSize, uintptr(fd), 0, iptr(_OFFSET_BEGINNING), dosBase, 0)
		if int32(r) < 0 && e != ERROR_ACTION_NOT_KNOWN && e != ERROR_NOT_IMPLEMENTED {
			Close(fd)
			return 0, e
		}
	}
	if mode&O_APPEND != 0 {
		appendLock.Lock()
		if appendFDs == nil {
			appendFDs = make(map[Handle]bool)
		}
		appendFDs[fd] = true
		appendLock.Unlock()
	}
	return fd, nil
}

func Close(fd Handle) error {
	appendLock.Lock()
	delete(appendFDs, fd)
	prot, pending := pendingProt[fd]
	delete(pendingProt, fd)
	appendLock.Unlock()
	var name string
	if pending {
		name, _ = NameFromFH(fd)
	}
	if r, e := dosCall(_LVO_Close, uintptr(fd), dosBase, 0, 0, 0); int16(r) == 0 {
		return e
	}
	if name != "" {
		return SetProtection(name, prot)
	}
	return nil
}

// Read reads at most 2 GB at a time (dos lengths are LONG).
func Read(fd Handle, p []byte) (n int, err error) {
	if fd == 0 {
		return 0, EINVAL // dos dereferences a BNULL handle (go/HANDOFF.md F21l)
	}
	if len(p) == 0 {
		return 0, nil
	}
	r, e := dosCall(_LVO_Read, uintptr(fd), uintptr(unsafe.Pointer(&p[0])), uintptr(min(len(p), 1<<31-1)), dosBase, 0)
	if int32(r) < 0 {
		return 0, e
	}
	return int(int32(r)), nil
}

func Write(fd Handle, p []byte) (n int, err error) {
	if fd == 0 {
		return 0, EINVAL
	}
	if len(p) == 0 {
		return 0, nil
	}
	if isAppend(fd) {
		if r, e := dosCall(_LVO_Seek, uintptr(fd), 0, iptr(_OFFSET_END), dosBase, 0); int32(r) < 0 {
			return 0, e
		}
	}
	r, e := dosCall(_LVO_Write, uintptr(fd), uintptr(unsafe.Pointer(&p[0])), uintptr(min(len(p), 1<<31-1)), dosBase, 0)
	if int32(r) < 0 {
		return 0, e
	}
	return int(int32(r)), nil
}

// Seek moves to offset (whence 0, 1, 2 as io.Seek*) and returns the new
// offset. dos offsets are 32-bit (go/HANDOFF.md F18b: no 64-bit packets on
// hosted), and the hosted handler refuses a seek past the end.
func Seek(fd Handle, offset int64, whence int) (newoffset int64, err error) {
	var mode int
	switch whence {
	case 0:
		mode = _OFFSET_BEGINNING
	case 1:
		mode = _OFFSET_CURRENT
	case 2:
		mode = _OFFSET_END
	default:
		return 0, EINVAL
	}
	if fd == 0 || offset != int64(int32(offset)) {
		return 0, EINVAL
	}
	if r, e := dosCall(_LVO_Seek, uintptr(fd), uintptr(offset), iptr(mode), dosBase, 0); int32(r) < 0 {
		return 0, e
	}
	// Seek returns the old position; ask for the new one.
	r, e := dosCall(_LVO_Seek, uintptr(fd), 0, iptr(_OFFSET_CURRENT), dosBase, 0)
	if int32(r) < 0 {
		return 0, e
	}
	return int64(int32(r)), nil
}

// Ftruncate sets fd's size to length. The position stays, except that
// ram-handler moves a position past the new end back to it (go/HANDOFF.md
// F24). A file grows by zeros Ftruncate writes itself: ram-handler's
// SetFileSize leaves old memory in the bytes it adds (F27).
func Ftruncate(fd Handle, length int64) error {
	if fd == 0 || length < 0 || length != int64(int32(length)) {
		return EINVAL
	}
	cur, err := Seek(fd, 0, 1)
	if err != nil {
		return err
	}
	end, err := Seek(fd, 0, 2)
	if err != nil {
		return err
	}
	if length > end {
		err = writeZeros(fd, length-end)
	} else if r, e := dosCall(_LVO_SetFileSize, uintptr(fd), uintptr(length), iptr(_OFFSET_BEGINNING), dosBase, 0); int32(r) < 0 {
		err = e
	}
	if cur > length {
		cur = length // as ram-handler does; the hosted handler can't seek past the end either
	}
	if _, e := Seek(fd, cur, 0); err == nil {
		err = e
	}
	return err
}

var zeros [4096]byte

// writeZeros writes n zero bytes at fd's position.
func writeZeros(fd Handle, n int64) error {
	for n > 0 {
		m := min(n, int64(len(zeros)))
		w, err := Write(fd, zeros[:m])
		if err != nil {
			return err
		}
		if w == 0 {
			return EIO
		}
		n -= int64(w)
	}
	return nil
}

// Lock locks path (mode SHARED_LOCK or EXCLUSIVE_LOCK, as LockShared and
// LockExclusive). UnLock it when done.
func Lock(path string, mode int) (Handle, error) {
	p, err := dosPath(path)
	if err != nil {
		return 0, err
	}
	r, e := dosCall(_LVO_Lock, uintptr(unsafe.Pointer(p)), iptr(mode), dosBase, 0, 0)
	if r == 0 {
		return 0, notFound(path, e)
	}
	return Handle(r), nil
}

// notFound returns e, the error of a dos call on path, except that a name
// under a file gives ENOTDIR, as on Unix: dos says only that the object
// isn't there (ERROR_OBJECT_NOT_FOUND on RAM:, ERROR_DIR_NOT_FOUND on SYS:
// for "file/x", go/HANDOFF.md F24f). It looks for the nearest parent that
// exists.
func notFound(path string, e Errno) Errno {
	if e != ERROR_OBJECT_NOT_FOUND && e != ERROR_DIR_NOT_FOUND {
		return e
	}
	for dir := parentPath(path); dir != ""; dir = parentPath(dir) {
		p, err := dosPath(dir)
		if err != nil {
			return e
		}
		l, _ := dosCall(_LVO_Lock, uintptr(unsafe.Pointer(p)), iptr(_SHARED_LOCK), dosBase, 0, 0)
		if l == 0 {
			continue
		}
		var fib FileInfoBlock
		err = Examine(Handle(l), &fib)
		dosCall(_LVO_UnLock, l, dosBase, 0, 0, 0)
		if err == nil && fib.DirEntryType < 0 {
			return ENOTDIR
		}
		return e
	}
	return e
}

// parentPath returns the Go path of path's parent directory: "a/b/" and
// "a/b" give "a", and a name with no parent before it ("a", "Work:a",
// "/a") gives "".
func parentPath(path string) string {
	vol := arosVolumeLen(path)
	i := len(path)
	for i > vol && path[i-1] == '/' {
		i--
	}
	for i > vol && path[i-1] != '/' {
		i--
	}
	for i > vol && path[i-1] == '/' {
		i--
	}
	if i <= vol {
		return ""
	}
	return path[:i]
}

const (
	LockShared    = _SHARED_LOCK
	LockExclusive = _EXCLUSIVE_LOCK
)

func UnLock(lock Handle) {
	dosCall(_LVO_UnLock, uintptr(lock), dosBase, 0, 0, 0)
}

// DupLock returns a second shared lock on what lock locks.
func DupLock(lock Handle) (Handle, error) {
	r, e := dosCall(_LVO_DupLock, uintptr(lock), dosBase, 0, 0, 0)
	if r == 0 {
		return 0, e
	}
	return Handle(r), nil
}

// A DateStamp is dos's time: days since 1978-01-01, minutes since
// midnight, ticks (1/50 s) in the minute, all on AROS's clock (host local
// time on hosted AROS, go/HANDOFF.md F19).
type DateStamp struct {
	Days, Minute, Tick int32
}

// A FileInfoBlock is what Examine, ExNext and ExamineFH fill (32-bit sizes:
// darwin-aarch64 builds dos without __DOS64). Check the layout below.
type FileInfoBlock struct {
	DiskKey      uintptr
	DirEntryType int32 // >= 0: directory, < 0: file
	FileName     [108]byte
	Protection   int32
	EntryType    int32
	Size         int32
	NumBlocks    int32
	Date         DateStamp
	Comment      [80]byte
	OwnerUID     uint16
	OwnerGID     uint16
	Reserved     [32]byte
}

// Compile-time checks of FileInfoBlock and DateStamp against the SDK
// (zdefs_aros_arm64.go): a mismatch makes an array length negative.
var (
	_ [unsafe.Sizeof(FileInfoBlock{}) - _sizeof_FileInfoBlock]byte
	_ [_sizeof_FileInfoBlock - unsafe.Sizeof(FileInfoBlock{})]byte
	_ [unsafe.Offsetof(FileInfoBlock{}.DirEntryType) - _FileInfoBlock_fib_DirEntryType]byte
	_ [_FileInfoBlock_fib_DirEntryType - unsafe.Offsetof(FileInfoBlock{}.DirEntryType)]byte
	_ [unsafe.Offsetof(FileInfoBlock{}.FileName) - _FileInfoBlock_fib_FileName]byte
	_ [_FileInfoBlock_fib_FileName - unsafe.Offsetof(FileInfoBlock{}.FileName)]byte
	_ [unsafe.Offsetof(FileInfoBlock{}.Protection) - _FileInfoBlock_fib_Protection]byte
	_ [_FileInfoBlock_fib_Protection - unsafe.Offsetof(FileInfoBlock{}.Protection)]byte
	_ [unsafe.Offsetof(FileInfoBlock{}.Size) - _FileInfoBlock_fib_Size]byte
	_ [_FileInfoBlock_fib_Size - unsafe.Offsetof(FileInfoBlock{}.Size)]byte
	_ [unsafe.Offsetof(FileInfoBlock{}.Date) - _FileInfoBlock_fib_Date]byte
	_ [_FileInfoBlock_fib_Date - unsafe.Offsetof(FileInfoBlock{}.Date)]byte
	_ [unsafe.Offsetof(FileInfoBlock{}.Comment) - _FileInfoBlock_fib_Comment]byte
	_ [_FileInfoBlock_fib_Comment - unsafe.Offsetof(FileInfoBlock{}.Comment)]byte
	_ [unsafe.Sizeof(DateStamp{}) - _sizeof_DateStamp]byte
	_ [_sizeof_DateStamp - unsafe.Sizeof(DateStamp{})]byte
)

// Name returns fib's file name.
func (fib *FileInfoBlock) Name() string { return cstring(fib.FileName[:]) }

func cstring(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

// Examine fills fib for lock.
func Examine(lock Handle, fib *FileInfoBlock) error {
	if r, e := dosCall(_LVO_Examine, uintptr(lock), uintptr(unsafe.Pointer(fib)), dosBase, 0, 0); int32(r) == 0 {
		return e
	}
	return nil
}

// ExNext fills fib with the next entry of directory lock, after Examine;
// ERROR_NO_MORE_ENTRIES ends the list.
func ExNext(lock Handle, fib *FileInfoBlock) error {
	if r, e := dosCall(_LVO_ExNext, uintptr(lock), uintptr(unsafe.Pointer(fib)), dosBase, 0, 0); int32(r) == 0 {
		return e
	}
	return nil
}

// ExamineFH fills fib for an open file.
func ExamineFH(fd Handle, fib *FileInfoBlock) error {
	if fd == 0 {
		return EINVAL
	}
	if r, e := dosCall(_LVO_ExamineFH, uintptr(fd), uintptr(unsafe.Pointer(fib)), dosBase, 0, 0); int16(r) == 0 {
		return e
	}
	return nil
}

// Mkdir creates directory path with perm (permProt), as Open does.
func Mkdir(path string, perm uint32) error {
	p, err := dosPath(path)
	if err != nil {
		return err
	}
	r, e := dosCall(_LVO_CreateDir, uintptr(unsafe.Pointer(p)), dosBase, 0, 0, 0)
	if r == 0 {
		return notFound(path, e)
	}
	UnLock(Handle(r)) // CreateDir returns an exclusive lock on the new directory
	dosCall(_LVO_SetProtection, uintptr(unsafe.Pointer(p)), uintptr(permProt(perm)), dosBase, 0, 0)
	return nil
}

// dosObject is dosPath for a call that changes the object itself: the
// current directory, which arosPath gives as "", is EINVAL, as Unix
// refuses rmdir(".") and rename(".", x) (go/HANDOFF.md F22e); so is a path
// whose last name is "." or "..", which arosPath would clean to the
// directory itself ("a/." to "a").
func dosObject(path string) (*byte, error) {
	if e := lastName(path); e == "." || e == ".." {
		return nil, EINVAL
	}
	p, err := arosPath(path)
	if err != nil {
		return nil, err
	}
	if p == "" {
		return nil, EINVAL
	}
	return BytePtrFromString(p)
}

// lastName returns path's last name: "c" for "a/b/c" and "a/b/c/".
func lastName(path string) string {
	vol := arosVolumeLen(path)
	i := len(path)
	for i > vol && path[i-1] == '/' {
		i--
	}
	j := i
	for j > vol && path[j-1] != '/' {
		j--
	}
	return path[j:i]
}

// Remove deletes a file or an empty directory (dos DeleteFile does both).
func Remove(path string) error {
	p, err := dosObject(path)
	if err != nil {
		return err
	}
	if r, e := dosCall(_LVO_DeleteFile, uintptr(unsafe.Pointer(p)), dosBase, 0, 0, 0); int16(r) == 0 {
		return notFound(path, e)
	}
	return nil
}

// Rename renames from to to. dos never overwrites an existing to
// (ERROR_OBJECT_EXISTS); package os deletes it first (go/HANDOFF.md F18b).
func Rename(from, to string) error {
	p, err := dosObject(from)
	if err != nil {
		return err
	}
	q, err := dosObject(to)
	if err != nil {
		return err
	}
	if r, e := dosCall(_LVO_Rename, uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(q)), dosBase, 0, 0); int32(r) == 0 {
		if e = notFound(from, e); e != ENOTDIR {
			e = notFound(to, e)
		}
		return e
	}
	return nil
}

// dosCall6 is dosCall for a function with five arguments (ReadLink); the
// caller passes dosBase as a5.
//
//go:nosplit
//go:uintptrkeepalive
func dosCall6(lvo, a0, a1, a2, a3, a4, a5 uintptr) (uintptr, Errno) {
	r, e := aroscall(*(*uintptr)(unsafe.Pointer(dosBase - 8*lvo)), a0, a1, a2, a3, a4, a5)
	return r, Errno(e)
}

// Soft links (go/HANDOFF.md D31, D33). dos ReadLink asks the handler that
// owns a lock for a link named relative to that lock. readLink asks for
// the last name only, relative to its parent's lock: ram-handler resolves
// a link in the middle of the name, the hosted handler doesn't (F26e).

// readLink reads soft link path: its target as dos keeps it, and, given
// fib, its entry in the parent directory and the parent's full name (a Go
// path). A name that isn't a soft link gives EINVAL, as readlink(2).
func readLink(path string, fib *FileInfoBlock) (target, dir string, err error) {
	p, err := arosPath(path)
	if err != nil {
		return "", "", err
	}
	d, name := splitDosPath(p)
	if name == "" {
		return "", "", EINVAL // the current directory, a parent or a root
	}
	dp, err := BytePtrFromString(d)
	if err != nil {
		return "", "", err
	}
	np, err := BytePtrFromString(name)
	if err != nil {
		return "", "", err
	}
	lock, e := dosCall(_LVO_Lock, uintptr(unsafe.Pointer(dp)), iptr(_SHARED_LOCK), dosBase, 0, 0)
	if lock == 0 {
		return "", "", notFound(path, e)
	}
	defer UnLock(Handle(lock))
	port := *(*uintptr)(unsafe.Pointer(lock + _FileLock_fl_Task))
	for n := 256; ; n *= 2 {
		buf := make([]byte, n)
		r, e := dosCall6(_LVO_ReadLink, port, lock, uintptr(unsafe.Pointer(np)), uintptr(unsafe.Pointer(&buf[0])), uintptr(n), dosBase)
		switch int32(r) {
		case -1:
			if e == ERROR_OBJECT_NOT_FOUND || e == ERROR_DIR_NOT_FOUND {
				return "", "", notFound(path, e)
			}
			// Not a link (ram-handler says ERROR_OBJECT_WRONG_TYPE, the
			// hosted handler 100), unless the name can't be locked either.
			pp, _ := BytePtrFromString(p)
			l, e := dosCall(_LVO_Lock, uintptr(unsafe.Pointer(pp)), iptr(_SHARED_LOCK), dosBase, 0, 0)
			if l == 0 {
				return "", "", notFound(path, e)
			}
			dosCall(_LVO_UnLock, l, dosBase, 0, 0, 0)
			return "", "", EINVAL
		case -2:
			// The buffer is too small (IoErr ERROR_BUFFER_OVERFLOW on
			// ram-handler, 0 on the hosted handler).
			if n >= 1<<16 {
				return "", "", ENAMETOOLONG
			}
			continue
		}
		target = cstring(buf[:r])
		break
	}
	if fib == nil {
		return target, "", nil
	}
	if err := Examine(Handle(lock), fib); err != nil {
		return "", "", err
	}
	for {
		if err := ExNext(Handle(lock), fib); err != nil {
			if err == ERROR_NO_MORE_ENTRIES {
				err = ENOENT // removed since ReadLink
			}
			return "", "", err
		}
		if equalFoldASCII(fib.Name(), name) {
			break
		}
	}
	dir, err = NameFromLock(Handle(lock))
	if err != nil {
		return "", "", err
	}
	return target, dir, nil
}

// equalFoldASCII reports whether a and b are the same dos name: dos ignores
// the case of ASCII letters.
func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

// Readlink returns the target of soft link path as a Go path (linkPath).
func Readlink(path string) (string, error) {
	t, _, err := readLink(path, nil)
	if err != nil {
		return "", err
	}
	return linkPath(t)
}

// LstatLink fills fib for soft link path from its entry in the parent
// directory (a link can't be locked: dos follows it) and returns the
// link's target, as Readlink, and the parent's full name. A name that
// isn't a soft link gives EINVAL.
func LstatLink(path string, fib *FileInfoBlock) (target, dir string, err error) {
	t, dir, err := readLink(path, fib)
	if err != nil {
		return "", "", err
	}
	t, err = linkPath(t)
	return t, dir, err
}

// Symlink makes newpath a soft link to oldpath (dos MakeLink, LINK_SOFT),
// with oldpath translated name by name (linkTarget), so Readlink gives it
// back. An existing link at newpath is EEXIST (dos says
// ERROR_IS_SOFT_LINK).
func Symlink(oldpath, newpath string) error {
	t, err := linkTarget(oldpath)
	if err != nil {
		return err
	}
	q, err := dosObject(newpath)
	if err != nil {
		return err
	}
	tp, err := BytePtrFromString(t)
	if err != nil {
		return err
	}
	if r, e := dosCall(_LVO_MakeLink, uintptr(unsafe.Pointer(q)), uintptr(unsafe.Pointer(tp)), _LINK_SOFT, dosBase, 0); int32(r) == 0 {
		if e == ERROR_IS_SOFT_LINK {
			return ERROR_OBJECT_EXISTS // newpath is a soft link
		}
		return notFound(newpath, e)
	}
	return nil
}

// SetProtection sets path's dos protection bits (RWED active-low, the
// rest active-high).
func SetProtection(path string, mask uint32) error {
	p, err := dosPath(path)
	if err != nil {
		return err
	}
	if r, e := dosCall(_LVO_SetProtection, uintptr(unsafe.Pointer(p)), uintptr(mask), dosBase, 0, 0); int32(r) == 0 {
		return e
	}
	return nil
}

// Unix permission bits and dos protection bits. The owner's RWE bits are
// active-low, the group's and others' active-high; D (delete) stays
// allowed, as Unix decides deletion by the directory. Reading follows
// posixc (__prot_a2u): S (script) makes a file executable for everyone.
const permProtMask = FIBF_READ | FIBF_WRITE | FIBF_EXECUTE | FIBF_GRP_READ | FIBF_GRP_WRITE | FIBF_GRP_EXECUTE |
	FIBF_OTR_READ | FIBF_OTR_WRITE | FIBF_OTR_EXECUTE

// permProt returns the protection bits for Unix permission bits perm.
func permProt(perm uint32) uint32 {
	var prot uint32
	if perm&0400 == 0 {
		prot |= FIBF_READ
	}
	if perm&0200 == 0 {
		prot |= FIBF_WRITE
	}
	if perm&0100 == 0 {
		prot |= FIBF_EXECUTE
	}
	for _, b := range [...]struct{ perm, prot uint32 }{
		{0040, FIBF_GRP_READ}, {0020, FIBF_GRP_WRITE}, {0010, FIBF_GRP_EXECUTE},
		{0004, FIBF_OTR_READ}, {0002, FIBF_OTR_WRITE}, {0001, FIBF_OTR_EXECUTE},
	} {
		if perm&b.perm != 0 {
			prot |= b.prot
		}
	}
	return prot
}

// Perm returns fib's protection as Unix permission bits.
func (fib *FileInfoBlock) Perm() uint32 {
	prot := uint32(fib.Protection)
	var perm uint32
	if prot&FIBF_SCRIPT != 0 {
		perm |= 0111
	}
	if prot&FIBF_EXECUTE == 0 {
		perm |= 0100
	}
	if prot&FIBF_WRITE == 0 {
		perm |= 0200
	}
	if prot&FIBF_READ == 0 {
		perm |= 0400
	}
	for _, b := range [...]struct{ perm, prot uint32 }{
		{0040, FIBF_GRP_READ}, {0020, FIBF_GRP_WRITE}, {0010, FIBF_GRP_EXECUTE},
		{0004, FIBF_OTR_READ}, {0002, FIBF_OTR_WRITE}, {0001, FIBF_OTR_EXECUTE},
	} {
		if prot&b.prot != 0 {
			perm |= b.perm
		}
	}
	return perm
}

// Chmod sets path's permission bits to perm, keeping its other protection
// bits (D, A, P, S, H).
func Chmod(path string, perm uint32) error {
	lock, err := Lock(path, LockShared)
	if err != nil {
		return err
	}
	var fib FileInfoBlock
	err = Examine(lock, &fib)
	UnLock(lock)
	if err != nil {
		return err
	}
	return SetProtection(path, uint32(fib.Protection)&^permProtMask|permProt(perm))
}

// SetFileDate sets path's date.
func SetFileDate(path string, ds *DateStamp) error {
	p, err := dosPath(path)
	if err != nil {
		return err
	}
	if r, e := dosCall(_LVO_SetFileDate, uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(ds)), dosBase, 0, 0); int16(r) == 0 {
		return e
	}
	return nil
}

// NameFromLock returns lock's full name as a Go path (goPath).
func NameFromLock(lock Handle) (string, error) {
	return nameFrom(_LVO_NameFromLock, uintptr(lock))
}

// NameFromFH returns the full name of fd's file as a Go path.
func NameFromFH(fd Handle) (string, error) {
	if fd == 0 {
		return "", EINVAL
	}
	return nameFrom(_LVO_NameFromFH, uintptr(fd))
}

// nameFrom calls NameFromLock or NameFromFH with a buffer that grows until
// the name fits.
func nameFrom(lvo, arg uintptr) (string, error) {
	// A name dos takes is at most 255 bytes (maxPathLen), but the full name
	// can be longer ("RAM:" is "RAM Disk:"). Start big: a NameFromLock
	// that runs out of buffer keeps a parent locked, and its UnLock clears
	// the ERROR_LINE_TOO_LONG it set (rom/dos/namefrom.c, go/HANDOFF.md
	// F27), so 0 also means too long.
	for n := 1024; ; n *= 2 {
		buf := make([]byte, n)
		r, e := dosCall(lvo, arg, uintptr(unsafe.Pointer(&buf[0])), uintptr(n), dosBase, 0)
		if int16(r) != 0 {
			return goPath(cstring(buf))
		}
		if e != ERROR_LINE_TOO_LONG && e != 0 || n >= 1<<16 {
			return "", e
		}
	}
}

// cwdLock keeps Chdir from unlocking the current directory while Getwd
// names it: NameFromLock allocates before its dos call, and a goroutine
// switch there would leave Getwd with a freed lock.
var cwdLock sync.Mutex

// Getwd returns the current directory as a Go path ("Work:dir").
func Getwd() (string, error) {
	cwdLock.Lock()
	defer cwdLock.Unlock()
	return NameFromLock(*(*Handle)(unsafe.Pointer(findTask() + _Process_pr_CurrentDir)))
}

// Chdir makes path the current directory. The lock it replaces is unlocked
// unless it is the shell's, which the runtime gives back at exit (along
// with unlocking Go's last one; go/HANDOFF.md D18).
func Chdir(path string) error {
	lock, err := Lock(path, LockShared)
	if err != nil {
		return err
	}
	var fib FileInfoBlock
	if err := Examine(lock, &fib); err != nil {
		UnLock(lock)
		return err
	}
	if fib.DirEntryType < 0 {
		UnLock(lock)
		return ENOTDIR
	}
	setCurrentDir(lock)
	return nil
}

// Fchdir makes the directory lock locks the current directory. It takes a
// lock of its own (DupLock): the caller keeps lock.
func Fchdir(lock Handle) error {
	var fib FileInfoBlock
	if err := Examine(lock, &fib); err != nil {
		return err
	}
	if fib.DirEntryType < 0 {
		return ENOTDIR
	}
	l, err := DupLock(lock)
	if err != nil {
		return err
	}
	setCurrentDir(l)
	return nil
}

// setCurrentDir makes lock, which it takes over, the current directory.
func setCurrentDir(lock Handle) {
	cwdLock.Lock()
	old, _ := dosCall(_LVO_CurrentDir, uintptr(lock), dosBase, 0, 0, 0)
	if old != arosShellDir() {
		UnLock(Handle(old))
	}
	cwdLock.Unlock()
}

// GetProgramDir returns the lock on the program's directory (PROGDIR:). It
// belongs to the shell: don't UnLock it.
func GetProgramDir() Handle {
	r, _ := dosCall(_LVO_GetProgramDir, dosBase, 0, 0, 0, 0)
	return Handle(r)
}

// GetProgramName returns the program's name as the shell gave it, as a Go
// path: absolute ("SYS:x/prog") or relative to GetProgramDir.
func GetProgramName() (string, error) {
	var buf [256]byte
	if r, e := dosCall(_LVO_GetProgramName, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), dosBase, 0, 0); int16(r) == 0 {
		return "", e
	}
	return goPath(cstring(buf[:]))
}
