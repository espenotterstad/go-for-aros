// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build aros

package runtime

// AROS has no Unix signals. Faults reach tc_TrapCode (F1) and preemption is a
// context injection (A9): both milestone 2-3. Until then these are inert.

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

func sigpanic() { throw("aros: fault handling: milestone 2") }

// os/signal's runtime side (sigqueue.go is excluded, as on Plan 9).
func signal_enable(s uint32)       {}
func signal_disable(s uint32)      {}
func signal_ignore(s uint32)       {}
func signal_ignored(s uint32) bool { return true }
func signal_recv() uint32          { return 0 }
