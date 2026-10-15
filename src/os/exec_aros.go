// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package os

import (
	"internal/strconv"
	"syscall"
	"time"
)

// AROS starts no child processes yet (go/HANDOFF.md D3: no fork or exec):
// StartProcess fails with an error that is [errors.ErrUnsupported], and a
// Process found by FindProcess can't be signalled or waited for.

// The only signal values guaranteed to be present in the os package on all
// systems are Interrupt (send the process an interrupt) and Kill (force
// the process to exit). AROS delivers neither yet.
var (
	Interrupt Signal = syscall.SIGINT
	Kill      Signal = syscall.SIGKILL
)

func startProcess(name string, argv []string, attr *ProcAttr) (p *Process, err error) {
	return nil, &PathError{Op: "fork/exec", Path: name, Err: syscall.ENOSYS}
}

func (p *Process) signal(sig Signal) error {
	switch p.pidStatus() {
	case statusDone:
		return ErrProcessDone
	case statusReleased:
		return syscall.ENOENT
	}
	return NewSyscallError("signal", syscall.ENOSYS)
}

func (p *Process) kill() error {
	return p.signal(Kill)
}

func (p *Process) wait() (ps *ProcessState, err error) {
	if p.pidStatus() == statusReleased {
		return nil, ErrInvalid
	}
	return nil, NewSyscallError("wait", syscall.ENOSYS)
}

func (p *Process) withHandle(_ func(handle uintptr)) error {
	return ErrNoHandle
}

func findProcess(pid int) (p *Process, err error) {
	return newPIDProcess(pid), nil
}

// ProcessState stores information about a process, as reported by Wait.
// On AROS no Wait succeeds, so none is ever filled.
type ProcessState struct {
	pid  int // The process's id.
	code int // Its return code.
}

// Pid returns the process id of the exited process.
func (p *ProcessState) Pid() int {
	return p.pid
}

func (p *ProcessState) exited() bool {
	return true
}

func (p *ProcessState) success() bool {
	return p.code == 0
}

func (p *ProcessState) sys() any {
	return nil
}

func (p *ProcessState) sysUsage() any {
	return nil
}

func (p *ProcessState) userTime() time.Duration {
	return 0
}

func (p *ProcessState) systemTime() time.Duration {
	return 0
}

func (p *ProcessState) String() string {
	if p == nil {
		return "<nil>"
	}
	return "exit status " + strconv.Itoa(p.code)
}

// ExitCode returns the exit code of the exited process, or -1
// if the process hasn't exited or was terminated by a signal.
func (p *ProcessState) ExitCode() int {
	if p == nil {
		return -1
	}
	return p.code
}
