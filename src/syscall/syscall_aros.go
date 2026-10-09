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

func stdHandle(lvo uintptr) Handle {
	r, _ := dosCall(lvo, dosBase, 0, 0, 0, 0)
	return Handle(r)
}

func stderrHandle() Handle {
	t, _ := aroscall(*(*uintptr)(unsafe.Pointer(sysBase - 8*_LVO_FindTask)), 0, sysBase, 0, 0, 0, 0)
	if h := *(*Handle)(unsafe.Pointer(t + _Process_pr_CES)); h != 0 {
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
var (
	appendLock sync.Mutex
	appendFDs  map[Handle]bool
)

func isAppend(fd Handle) bool {
	appendLock.Lock()
	defer appendLock.Unlock()
	return appendFDs[fd]
}

// Open opens path. perm is ignored: dos gives a new file its default
// protection. O_CREAT|O_EXCL checks with Lock first (not atomic). Every
// create is MODE_READWRITE (open or create, a shared lock), O_TRUNC a
// SetFileSize after it: MODE_NEWFILE would lock the file exclusively while
// it is open and, on ram-handler, delete and recreate an existing one.
func Open(path string, mode int, perm uint32) (fd Handle, err error) {
	p, err := dosPath(path)
	if err != nil {
		return 0, err
	}
	dmode := uintptr(_MODE_OLDFILE) // existing file, read and write
	if mode&O_CREAT != 0 {
		if mode&O_EXCL != 0 {
			if l, _ := dosCall(_LVO_Lock, uintptr(unsafe.Pointer(p)), iptr(_SHARED_LOCK), dosBase, 0, 0); l != 0 {
				dosCall(_LVO_UnLock, l, dosBase, 0, 0, 0)
				return 0, EEXIST
			}
		}
		dmode = _MODE_READWRITE
	}
	r, e := dosCall(_LVO_Open, uintptr(unsafe.Pointer(p)), dmode, dosBase, 0, 0)
	if r == 0 {
		return 0, e
	}
	fd = Handle(r)
	if mode&O_TRUNC != 0 {
		if r, e := dosCall(_LVO_SetFileSize, uintptr(fd), 0, iptr(_OFFSET_BEGINNING), dosBase, 0); int32(r) < 0 {
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
	appendLock.Unlock()
	if r, e := dosCall(_LVO_Close, uintptr(fd), dosBase, 0, 0, 0); int16(r) == 0 {
		return e
	}
	return nil
}

// Read reads at most 2 GB at a time (dos lengths are LONG).
func Read(fd Handle, p []byte) (n int, err error) {
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
	if offset != int64(int32(offset)) {
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

// Lock locks path (mode SHARED_LOCK or EXCLUSIVE_LOCK, as LockShared and
// LockExclusive). UnLock it when done.
func Lock(path string, mode int) (Handle, error) {
	p, err := dosPath(path)
	if err != nil {
		return 0, err
	}
	r, e := dosCall(_LVO_Lock, uintptr(unsafe.Pointer(p)), iptr(mode), dosBase, 0, 0)
	if r == 0 {
		return 0, e
	}
	return Handle(r), nil
}

const (
	LockShared    = _SHARED_LOCK
	LockExclusive = _EXCLUSIVE_LOCK
)

func UnLock(lock Handle) {
	dosCall(_LVO_UnLock, uintptr(lock), dosBase, 0, 0, 0)
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
	if r, e := dosCall(_LVO_ExamineFH, uintptr(fd), uintptr(unsafe.Pointer(fib)), dosBase, 0, 0); int16(r) == 0 {
		return e
	}
	return nil
}

// Mkdir creates directory path. perm is ignored, as in Open.
func Mkdir(path string, perm uint32) error {
	p, err := dosPath(path)
	if err != nil {
		return err
	}
	r, e := dosCall(_LVO_CreateDir, uintptr(unsafe.Pointer(p)), dosBase, 0, 0, 0)
	if r == 0 {
		return e
	}
	UnLock(Handle(r)) // CreateDir returns an exclusive lock on the new directory
	return nil
}

// Remove deletes a file or an empty directory (dos DeleteFile does both).
func Remove(path string) error {
	p, err := dosPath(path)
	if err != nil {
		return err
	}
	if r, e := dosCall(_LVO_DeleteFile, uintptr(unsafe.Pointer(p)), dosBase, 0, 0, 0); int16(r) == 0 {
		return e
	}
	return nil
}

// Rename renames from to to. dos never overwrites an existing to
// (ERROR_OBJECT_EXISTS); package os deletes it first (go/HANDOFF.md F18b).
func Rename(from, to string) error {
	p, err := dosPath(from)
	if err != nil {
		return err
	}
	q, err := dosPath(to)
	if err != nil {
		return err
	}
	if r, e := dosCall(_LVO_Rename, uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(q)), dosBase, 0, 0); int32(r) == 0 {
		return e
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
	for n := 256; ; n *= 2 {
		buf := make([]byte, n)
		r, e := dosCall(_LVO_NameFromLock, uintptr(lock), uintptr(unsafe.Pointer(&buf[0])), uintptr(n), dosBase, 0)
		if int16(r) != 0 {
			return goPath(cstring(buf))
		}
		if e != ERROR_LINE_TOO_LONG || n >= 1<<16 {
			return "", e
		}
	}
}

// Getwd returns the current directory as a Go path ("Work:dir").
func Getwd() (string, error) {
	t, _ := aroscall(*(*uintptr)(unsafe.Pointer(sysBase - 8*_LVO_FindTask)), 0, sysBase, 0, 0, 0, 0)
	return NameFromLock(*(*Handle)(unsafe.Pointer(t + _Process_pr_CurrentDir)))
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
	old, _ := dosCall(_LVO_CurrentDir, uintptr(lock), dosBase, 0, 0, 0)
	if old != arosShellDir() {
		UnLock(Handle(old))
	}
	return nil
}
