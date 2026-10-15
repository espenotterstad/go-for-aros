// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package poll

import "syscall"

// AROS has no poller and package os doesn't use FD for its files (as on
// Plan 9): FD exists for the shared code in fd_mutex.go, and FDMutex is
// what os locks a file with.

// FD is a file descriptor; on AROS nothing opens one.
type FD struct {
	// Lock sysfd and serialize access to Read and Write methods.
	fdmu fdMutex

	// Whether this is a normal file.
	isFile bool
}

func (fd *FD) destroy() error {
	return nil
}

// FDMutex is an fdMutex for package os.
type FDMutex struct {
	fdmu fdMutex
}

func (fdmu *FDMutex) Incref() bool {
	return fdmu.fdmu.incref()
}

func (fdmu *FDMutex) Decref() bool {
	return fdmu.fdmu.decref()
}

func (fdmu *FDMutex) IncrefAndClose() bool {
	return fdmu.fdmu.increfAndClose()
}

func (fdmu *FDMutex) ReadLock() bool {
	return fdmu.fdmu.rwlock(readlock, waitLock)
}

func (fdmu *FDMutex) ReadUnlock() bool {
	return fdmu.fdmu.rwunlock(readlock)
}

func (fdmu *FDMutex) WriteLock() bool {
	return fdmu.fdmu.rwlock(writeLock, waitLock)
}

func (fdmu *FDMutex) WriteUnlock() bool {
	return fdmu.fdmu.rwunlock(writeLock)
}

// IsPollDescriptor reports whether fd is the descriptor being used by the
// poller; there is none.
func IsPollDescriptor(fd uintptr) bool {
	return false
}

// DupCloseOnExec would duplicate fd; dos can't duplicate a file handle.
func DupCloseOnExec(fd int) (int, string, error) {
	return -1, "dup", syscall.ENOSYS
}
