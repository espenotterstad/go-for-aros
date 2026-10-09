// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build aros

package runtime

import (
	"internal/abi"
	"internal/runtime/atomic"
	"unsafe"
)

// The C startup's library bases. Only rt0_aros_arm64.s reads the C symbols;
// Go code uses the copies main stores in arosSysBase and arosDOSBase.
//
//go:cgo_import_static SysBase
//go:cgo_import_static DOSBase

const arosG0StackSize = 128 << 10 // go/HANDOFF.md D11

var (
	arosSysBase, arosDOSBase                uintptr
	arosTask                                uintptr    // the main task (process)
	arosG0Stack                             uintptr    // AllocMem'd g0 stack, freed by arosLeave
	arosCSave                               [23]uint64 // C registers, SP and stack bounds saved by main
	arosHeapBase, arosHeapSize, arosHeapEnd uintptr
	arosTimerReq                            [10]uint64 // struct timerequest (80 bytes)
	arosTimerOpen                           uint32
	arosTimerBase                           uintptr
	arosTimerName                           = [...]byte{'t', 'i', 'm', 'e', 'r', '.', 'd', 'e', 'v', 'i', 'c', 'e', 0}
	arosOldTrap                             uintptr // the task's tc_TrapCode before main, restored by arosLeave
	arosFaulted                             uint32  // set by arosTrap when it sends a fault to arosFault
	arosFaultCode, arosFaultPC              uintptr
	arosTrapDepth                           uintptr // deepest trap frame below a faulting goroutine's sp so far (D12)
)

// cstr returns the address of s's bytes for an AROS call; s must end in "\x00".
func cstr(s string) uintptr { return uintptr(unsafe.Pointer(unsafe.StringData(s))) }

type aroscallArgs struct {
	fn  uintptr
	a   [6]uintptr
	ret uintptr
}

func aroscall_trampoline()

// aroscall calls the C function fn(a0..a5) on g0. Pass unused arguments as 0.
//
//go:nosplit
func aroscall(fn, a0, a1, a2, a3, a4, a5 uintptr) uintptr {
	args := aroscallArgs{fn: fn, a: [6]uintptr{a0, a1, a2, a3, a4, a5}}
	asmcgocall(unsafe.Pointer(abi.FuncPCABI0(aroscall_trampoline)), noescape(unsafe.Pointer(&args)))
	return args.ret
}

// arosvec returns library function number lvo of base.
//
//go:nosplit
func arosvec(base, lvo uintptr) uintptr {
	return *(*uintptr)(unsafe.Pointer(base - 8*lvo))
}

type mOS struct {
	task   uintptr // exec task running this M
	sigbit int32   // exec signal for semasleep + 1; 0 = not allocated yet
	tbusy  uint32  // 1 while treq is at timer.device (arosLeave aborts it)
	port   uintptr // treq's reply port; it signals the task that created it
	treq   uintptr // struct timerequest for timed waits, made by minit
}

type sigset struct{}
type gsignalStack struct{}

const _NSIG = 32

func osinit() {
	physPageSize = 4096
	numCPUStartup = getCPUCount()
	arosOpenTimer()
	arosAllocHeap()
}

func getCPUCount() int32 { return 1 } // one M until milestone 3

func arosOpenTimer() {
	if int8(aroscall(arosvec(arosSysBase, _LVO_OpenDevice),
		uintptr(unsafe.Pointer(&arosTimerName[0])), _UNIT_MICROHZ,
		uintptr(unsafe.Pointer(&arosTimerReq[0])), 0, arosSysBase, 0)) != 0 {
		throw("aros: cannot open timer.device")
	}
	arosTimerOpen = 1
	arosTimerBase = *(*uintptr)(unsafe.Pointer(uintptr(unsafe.Pointer(&arosTimerReq[0])) + _IORequest_io_Device))
}

// arosGetVar reads AROS variable name (NUL-terminated) into buf, local
// before global (GetVar flags 0). It returns the length, or -1 if unset.
func arosGetVar(name uintptr, buf []byte) int {
	n := int(int32(aroscall(arosvec(arosDOSBase, _LVO_GetVar), name,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, arosDOSBase, 0)))
	if n < 0 {
		return -1
	}
	return min(n, len(buf)-1)
}

// arosHeapMB reads GOAROSHEAP (megabytes); 64 if unset or not a number.
func arosHeapMB() uintptr {
	var buf [16]byte
	n := arosGetVar(cstr("GOAROSHEAP\x00"), buf[:])
	if n <= 0 {
		return 64
	}
	mb := uintptr(0)
	for _, c := range buf[:n] {
		if c < '0' || c > '9' {
			return 64
		}
		mb = mb*10 + uintptr(c-'0')
	}
	if mb == 0 {
		return 64
	}
	return mb
}

func arosAllocHeap() {
	size := arosHeapMB() << 20
	p := aroscall(arosvec(arosSysBase, _LVO_AllocMem), size, _MEMF_ANY|_MEMF_CLEAR, arosSysBase, 0, 0, 0)
	if p == 0 {
		throw("aros: cannot allocate the heap (set GOAROSHEAP to fewer megabytes)")
	}
	if p+size > 1<<heapAddrBits {
		throw("aros: heap block above the 40-bit heap address range")
	}
	arosHeapBase, arosHeapSize, arosHeapEnd = p, size, p+size
	bloc = alignUp(p, physPageSize)
	blocMax = bloc
}

// sbrk grows the break inside the AllocMem block (mem_sbrk.go).
func sbrk(n uintptr) unsafe.Pointer {
	bl := bloc
	n = memRound(n)
	if bl+n > arosHeapEnd {
		return nil
	}
	if bl+n > blocMax {
		blocMax = bl + n
	}
	bloc += n
	return unsafe.Pointer(bl)
}

// arosEnvVars are the variables the runtime reads itself (go/HANDOFF.md D19);
// package syscall reads all others live through GetVar.
var arosEnvVars = [...]string{"GODEBUG", "GOGC", "GOMAXPROCS", "GOTRACEBACK", "GOMEMLIMIT", "GOAROSHEAP"}

// goenvs fills envs with the arosEnvVars that are set. envs must not stay nil:
// gogetenv throws before env init (go/HANDOFF.md F13c).
func goenvs() {
	envs = make([]string, 0, len(arosEnvVars))
	var name [16]byte
	var buf [1024]byte
	for _, k := range arosEnvVars {
		copy(name[:], k)
		name[len(k)] = 0
		if n := arosGetVar(uintptr(unsafe.Pointer(&name[0])), buf[:]); n >= 0 {
			envs = append(envs, k+"="+string(buf[:n]))
		}
	}
}

func libpreinit() {}

func readRandom(r []byte) int { return 0 } // runtime falls back to its time-based seed

func mpreinit(mp *m) {
	mp.gsignal = malg(32 * 1024)
	mp.gsignal.m = mp
}

func minit() {
	mp := getg().m
	mp.task = aroscall(arosvec(arosSysBase, _LVO_FindTask), 0, arosSysBase, 0, 0, 0, 0)
	arosTimerInit(mp)
}

// arosTimerInit gives mp a reply port and a timerequest for timed waits. A
// port signals the task that created it, so each M makes its own, here on its
// own task. The request copies osinit's open one (same device and unit), so
// it needs no OpenDevice of its own. arosLeave frees m0's.
func arosTimerInit(mp *m) {
	mp.port = aroscall(arosvec(arosSysBase, _LVO_CreateMsgPort), arosSysBase, 0, 0, 0, 0, 0)
	if mp.port == 0 {
		throw("aros: cannot create a message port")
	}
	req := aroscall(arosvec(arosSysBase, _LVO_CreateIORequest), mp.port, _sizeof_timerequest, arosSysBase, 0, 0, 0)
	if req == 0 {
		throw("aros: cannot create a timerequest")
	}
	tr := uintptr(unsafe.Pointer(&arosTimerReq[0]))
	*(*uintptr)(unsafe.Pointer(req + _IORequest_io_Device)) = *(*uintptr)(unsafe.Pointer(tr + _IORequest_io_Device))
	*(*uintptr)(unsafe.Pointer(req + _IORequest_io_Unit)) = *(*uintptr)(unsafe.Pointer(tr + _IORequest_io_Unit))
	mp.treq = req
}

func unminit()       {}
func mdestroy(mp *m) {}

func newosproc(mp *m) { throw("aros: newosproc: one M until milestone 3") }

func newosproc0(stacksize uintptr, fn unsafe.Pointer) {
	throw("aros: newosproc0: one M until milestone 3")
}

func exitThread(wait *atomic.Uint32) { throw("aros: exitThread: one M until milestone 3") }

func exit(code int32) // sys_aros_arm64.s

// arosFatalFault reports a fault arosTrap couldn't turn into a panic (C code,
// runtime code on g0, a stack split, runtime.abort); arosFault then exits
// with crashExitCode. Runs on g0's stack.
func arosFatalFault() {
	print("fatal error: aros: unexpected fault (trap ", arosFaultCode, ") at pc=", hex(arosFaultPC), "\n")
}

func crash() { exit(crashExitCode) }

//go:nosplit
func osyield() {} // one M: nothing to yield to inside Go (milestone 3)

//go:nosplit
func osyield_no_g() {}

// semacreate gives the current M an exec signal bit (lock_sema.go calls it
// for getg().m only, before that M waits).
func semacreate(mp *m) {
	if mp.sigbit != 0 {
		return
	}
	bit := int8(aroscall(arosvec(arosSysBase, _LVO_AllocSignal), ^uintptr(0), arosSysBase, 0, 0, 0, 0))
	if bit < 0 {
		throw("aros: no free exec signal")
	}
	mp.sigbit = int32(bit) + 1
}

// semasleep waits for semawakeup, at most ns nanoseconds if ns >= 0: 0 when
// woken, -1 on timeout (lock_sema.go).
//
//go:nosplit
func semasleep(ns int64) int32 {
	mp := getg().m
	sem := uintptr(1) << uint(mp.sigbit-1)
	if ns < 0 {
		aroscall(arosvec(arosSysBase, _LVO_Wait), sem, arosSysBase, 0, 0, 0, 0)
		return 0
	}
	if mp.treq == 0 {
		throw("aros: timed semasleep before minit")
	}
	arosTimerSet(mp.treq, ns/1000)
	mp.tbusy = 1
	aroscall(arosvec(arosSysBase, _LVO_SendIO), mp.treq, arosSysBase, 0, 0, 0, 0)
	got := aroscall(arosvec(arosSysBase, _LVO_Wait), sem|arosPortMask(mp), arosSysBase, 0, 0, 0, 0)
	// AbortIO does nothing once the timer has fired; WaitIO then takes the
	// request off the reply port either way.
	aroscall(arosvec(arosSysBase, _LVO_AbortIO), mp.treq, arosSysBase, 0, 0, 0, 0)
	aroscall(arosvec(arosSysBase, _LVO_WaitIO), mp.treq, arosSysBase, 0, 0, 0, 0)
	mp.tbusy = 0
	arosClearPort(mp)
	if got&sem != 0 {
		return 0
	}
	return -1
}

// arosTimerSet makes req a TR_ADDREQUEST for usec microseconds.
//
//go:nosplit
func arosTimerSet(req uintptr, usec int64) {
	secs := usec / 1e6
	if secs > 1<<31-1 {
		secs = 1<<31 - 1 // tv_secs is a ULONG; 68 years is forever here
	}
	*(*uint16)(unsafe.Pointer(req + _IORequest_io_Command)) = _TR_ADDREQUEST
	*(*uint32)(unsafe.Pointer(req + _timerequest_tr_time)) = uint32(secs)
	*(*uint32)(unsafe.Pointer(req + _timerequest_tr_time + 4)) = uint32(usec % 1e6)
}

// arosPortMask is the signal mask of mp's reply port.
//
//go:nosplit
func arosPortMask(mp *m) uintptr {
	return uintptr(1) << *(*uint8)(unsafe.Pointer(mp.port + _MsgPort_mp_SigBit))
}

// arosClearPort clears the reply-port signal WaitIO leaves set when the
// request was already back (it only Waits for one still out).
//
//go:nosplit
func arosClearPort(mp *m) {
	aroscall(arosvec(arosSysBase, _LVO_SetSignal), 0, arosPortMask(mp), arosSysBase, 0, 0, 0)
}

//go:nosplit
func semawakeup(mp *m) {
	aroscall(arosvec(arosSysBase, _LVO_Signal), mp.task, 1<<uint(mp.sigbit-1), arosSysBase, 0, 0, 0)
}

// write1 writes to the AROS output (fd 1) or error stream (fd 2: the process's
// pr_CES, else Output()).
//
//go:nosplit
func write1(fd uintptr, p unsafe.Pointer, n int32) int32 {
	fh := uintptr(0)
	if fd == 2 {
		task := aroscall(arosvec(arosSysBase, _LVO_FindTask), 0, arosSysBase, 0, 0, 0, 0)
		fh = *(*uintptr)(unsafe.Pointer(task + _Process_pr_CES))
	}
	if fh == 0 {
		fh = aroscall(arosvec(arosDOSBase, _LVO_Output), arosDOSBase, 0, 0, 0, 0, 0)
	}
	return int32(aroscall(arosvec(arosDOSBase, _LVO_Write), fh, uintptr(p), uintptr(n), arosDOSBase, 0, 0))
}

type arosTimeval struct{ secs, micro uint32 }

// AROS's system time counts from 1978-01-01, Unix time from 1970-01-01.
const arosEpochOffset = 252460800

//go:nosplit
func nanotime1() int64 {
	var tv arosTimeval
	aroscall(arosvec(arosTimerBase, _LVO_GetUpTime), uintptr(noescape(unsafe.Pointer(&tv))), arosTimerBase, 0, 0, 0, 0)
	return int64(tv.secs)*1e9 + int64(tv.micro)*1e3
}

func walltime() (sec int64, nsec int32) {
	var tv arosTimeval
	aroscall(arosvec(arosTimerBase, _LVO_GetSysTime), uintptr(noescape(unsafe.Pointer(&tv))), arosTimerBase, 0, 0, 0, 0)
	return int64(tv.secs) + arosEpochOffset, int32(tv.micro) * 1000
}

// usleep waits on the M's timerequest (5 ms steps on hosted, F18b). Before
// minit it falls back to usleep_no_g.
//
//go:nosplit
func usleep(usec uint32) {
	mp := getg().m
	if mp == nil || mp.treq == 0 {
		usleep_no_g(usec)
		return
	}
	arosTimerSet(mp.treq, int64(usec))
	mp.tbusy = 1
	aroscall(arosvec(arosSysBase, _LVO_DoIO), mp.treq, arosSysBase, 0, 0, 0, 0)
	mp.tbusy = 0
	arosClearPort(mp)
}

// usleep_no_g sleeps in whole dos ticks (20 ms): without a g there is no M,
// so no timerequest.
//
//go:nosplit
func usleep_no_g(usec uint32) {
	ticks := (uintptr(usec)*_TICKS_PER_SECOND + 999999) / 1000000
	aroscall(arosvec(arosDOSBase, _LVO_Delay), ticks, arosDOSBase, 0, 0, 0, 0)
}

// Stubs so export_test.go type-checks (go vet runtime); nothing calls them on
// aros (windows has the same).
func open(name *byte, mode, perm int32) int32 {
	throw("unimplemented")
	return -1
}
func closefd(fd int32) int32 {
	throw("unimplemented")
	return -1
}
func read(fd int32, p unsafe.Pointer, n int32) int32 {
	throw("unimplemented")
	return -1
}
