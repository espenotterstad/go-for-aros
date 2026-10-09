// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package syscall

import (
	errorspkg "errors"
	"internal/oserror"
	"internal/strconv"
	"unsafe"
)

// An Errno is a dos error code as IoErr returns it (go/HANDOFF.md D17); the
// dos codes have names ERROR_* (zdefs_aros_arm64.go). Go's Unix names are
// the dos code where dos has the same error, else invented, above every
// dos code.
type Errno uintptr

const (
	ENOENT    = ERROR_OBJECT_NOT_FOUND
	EEXIST    = ERROR_OBJECT_EXISTS
	ENOTEMPTY = ERROR_DIRECTORY_NOT_EMPTY
	ENOSYS    = ERROR_NOT_IMPLEMENTED
)

const (
	EINVAL Errno = 1<<16 + iota
	ENOTDIR
)

var errorText = [...]string{
	EINVAL - EINVAL:  "invalid argument",
	ENOTDIR - EINVAL: "not a directory",
}

// Error returns dos's text for its codes (Fault), Go's for the invented
// ones.
func (e Errno) Error() string {
	if e == 0 {
		return "dos call failed without an IoErr"
	}
	if e >= EINVAL {
		if i := int(e - EINVAL); i < len(errorText) {
			return errorText[i]
		}
		return "errno " + strconv.Itoa(int(e))
	}
	var buf [_FAULT_MAX]byte
	dosCall(_LVO_Fault, uintptr(e), 0, uintptr(unsafe.Pointer(&buf[0])), _FAULT_MAX, dosBase)
	return cstring(buf[:])
}

func (e Errno) Is(target error) bool {
	switch target {
	case oserror.ErrPermission:
		return e == ERROR_DELETE_PROTECTED || e == ERROR_WRITE_PROTECTED || e == ERROR_READ_PROTECTED ||
			e == ERROR_DISK_WRITE_PROTECTED
	case oserror.ErrExist:
		return e == ERROR_OBJECT_EXISTS || e == ERROR_DIRECTORY_NOT_EMPTY
	case oserror.ErrNotExist:
		return e == ERROR_OBJECT_NOT_FOUND || e == ERROR_DEVICE_NOT_MOUNTED || e == ERROR_DIR_NOT_FOUND ||
			e == ERROR_NO_DISK
	case errorspkg.ErrUnsupported:
		return e == ERROR_ACTION_NOT_KNOWN || e == ERROR_NOT_IMPLEMENTED
	}
	return false
}
