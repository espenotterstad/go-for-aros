// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Process identity on AROS: no users or groups, no child processes (no
// fork or exec, go/HANDOFF.md D3) and no signal delivery.

package syscall

import (
	"internal/strconv"
	"unsafe"
)

// Getpid returns the task's unique id, as posixc's getpid does
// (ETask.et_UniqueID), or -1 for a task without an ETask.
func Getpid() int {
	t := findTask()
	if *(*uint8)(unsafe.Pointer(t + _Task_tc_Flags))&_TF_ETASK == 0 {
		return -1
	}
	et := *(*uintptr)(unsafe.Pointer(t + _Task_tc_ETask))
	return int(*(*uint32)(unsafe.Pointer(et + _ETask_et_UniqueID)))
}

func Getppid() int { return -1 }
func Getuid() int  { return -1 }
func Geteuid() int { return -1 }
func Getgid() int  { return -1 }
func Getegid() int { return -1 }

func Getgroups() (gids []int, err error) {
	return make([]int, 0), nil
}

// A Signal is a signal number, for os.Interrupt and os.Kill; AROS
// delivers none.
type Signal int

const (
	SIGINT  Signal = 2
	SIGKILL Signal = 9
	SIGTERM Signal = 15
)

func (s Signal) Signal() {}

func (s Signal) String() string {
	switch s {
	case SIGINT:
		return "interrupt"
	case SIGKILL:
		return "killed"
	case SIGTERM:
		return "terminated"
	}
	return "signal " + strconv.Itoa(int(s))
}

// SysProcAttr holds no attributes: AROS starts no child processes.
type SysProcAttr struct{}

// Implemented in the runtime (runtime/os_aros.go).
func gettimeofday() (sec int64, nsec int32)

// Gettimeofday returns the wall time in UTC.
func Gettimeofday(tv *Timeval) error {
	sec, nsec := gettimeofday()
	tv.Sec, tv.Usec = sec, int64(nsec/1000)
	return nil
}
