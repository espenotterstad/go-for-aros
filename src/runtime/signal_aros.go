// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build aros

package runtime

// AROS has no Unix signals. Faults reach tc_TrapCode (F1, arosTrap in
// sys_aros_arm64.s); preemption is a context injection (A9, milestone 3).
// Until then the rest is inert.

func initsig(preinit bool)           {}
func clearSignalHandlers()           {}
func sigblock(exiting bool)          {}
func sigsave(p *sigset)              {}
func msigrestore(sigmask sigset)     {}
func sigenable(sig uint32)           {}
func sigdisable(sig uint32)          {}
func sigignore(sig uint32)           {}
func signame(sig uint32) string      { return "" }
func setProcessCPUProfiler(hz int32) {}
func setThreadCPUProfiler(hz int32)  {}

const preemptMSupported = false

func preemptM(mp *m) {}

// The exec trap number hosted aarch64 AROS gives SIGSEGV and SIGBUS
// (arch/all-unix/kernel/cpu_aarch64.c).
const _AROS_TRAP_BUS = 2

// sigpanic turns a fault in Go code into a run-time panic. arosTrap set it up
// as a call from the faulting instruction, with the trap number in gp.sig and
// the pc in gp.sigpc.
func sigpanic() {
	gp := getg()
	if !canpanic() {
		throw("unexpected signal during runtime execution")
	}
	if gp.sig != _AROS_TRAP_BUS {
		print("aros: trap ", gp.sig, " at pc=", hex(gp.sigpc), "\n")
		throw("unexpected trap")
	}
	// No fault address on aarch64 AROS (go/HANDOFF.md A8): every memory
	// fault is reported as a nil dereference.
	panicmem()
}

// os/signal's runtime side (sigqueue.go is excluded, as on Plan 9).
func signal_enable(s uint32)       {}
func signal_disable(s uint32)      {}
func signal_ignore(s uint32)       {}
func signal_ignored(s uint32) bool { return true }
func signal_recv() uint32          { return 0 }
