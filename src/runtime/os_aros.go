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

// GOAROSHEAP bounds in megabytes (go/HANDOFF.md D10). A hello needs 12 MB
// (measured 2026-10-09); the block must end below 1<<heapAddrBits.
const (
	arosHeapDefaultMB = 64
	arosHeapMinMB     = 16
	arosHeapMaxMB     = 1 << (heapAddrBits - 20)
)

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
	arosUTCOffset                           int64   // UTC minus AROS's clock in seconds, set by osinit (D21)
	arosShellDir                            uintptr // the shell's pr_CurrentDir, saved by main, restored by arosLeave (D18)
	arosShellWindowPtr                      uintptr // the shell's pr_WindowPtr; Go runs with -1, no requesters (D18)
	arosShellTaskFlags                      uint8   // tc_Flags before main cleared TF_STACKCHK (D9)
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
	arosUTCOffset = arosFindUTCOffset()
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

// arosHeapMB reads GOAROSHEAP (megabytes): the default if unset or empty, and
// a message when it isn't a number or lies outside arosHeapMinMB..arosHeapMaxMB.
func arosHeapMB() uintptr {
	var buf [16]byte
	n := arosGetVar(cstr("GOAROSHEAP\x00"), buf[:])
	if n <= 0 {
		return arosHeapDefaultMB
	}
	s := unsafe.String(&buf[0], n)
	mb := uintptr(0)
	for _, c := range buf[:n] {
		if c < '0' || c > '9' {
			print("runtime: GOAROSHEAP=", s, " is not a number of megabytes; using ", arosHeapDefaultMB, "\n")
			return arosHeapDefaultMB
		}
		if mb <= arosHeapMaxMB {
			mb = mb*10 + uintptr(c-'0')
		}
	}
	if mb < arosHeapMinMB {
		print("runtime: GOAROSHEAP=", s, " is below ", arosHeapMinMB, " MB; using ", arosHeapMinMB, "\n")
		return arosHeapMinMB
	}
	if mb > arosHeapMaxMB {
		print("runtime: GOAROSHEAP=", s, " is above ", arosHeapMaxMB, " MB; using ", arosHeapMaxMB, "\n")
		return arosHeapMaxMB
	}
	return mb
}

func arosAllocHeap() {
	size := arosHeapMB() << 20
	p := aroscall(arosvec(arosSysBase, _LVO_AllocMem), size, _MEMF_ANY|_MEMF_CLEAR, arosSysBase, 0, 0, 0)
	if p == 0 {
		throw("aros: cannot allocate the heap (set GOAROSHEAP to fewer megabytes)")
	}
	// Set before the range check, so arosLeave frees the block if it throws.
	arosHeapBase, arosHeapSize, arosHeapEnd = p, size, p+size
	if p+size > 1<<heapAddrBits {
		throw("aros: heap block above the 40-bit heap address range")
	}
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
const arosEpochOffset = 252460800 // 2922 days

//go:nosplit
func nanotime1() int64 {
	var tv arosTimeval
	aroscall(arosvec(arosTimerBase, _LVO_GetUpTime), uintptr(noescape(unsafe.Pointer(&tv))), arosTimerBase, 0, 0, 0, 0)
	return int64(tv.secs)*1e9 + int64(tv.micro)*1e3
}

// arosLocalTime reads AROS's clock, which runs on local time (F19), in
// seconds since the Unix epoch.
//
//go:nosplit
func arosLocalTime() (sec int64, nsec int32) {
	var tv arosTimeval
	aroscall(arosvec(arosTimerBase, _LVO_GetSysTime), uintptr(noescape(unsafe.Pointer(&tv))), arosTimerBase, 0, 0, 0, 0)
	return int64(tv.secs) + arosEpochOffset, int32(tv.micro) * 1000
}

func walltime() (sec int64, nsec int32) {
	sec, nsec = arosLocalTime()
	return sec + arosUTCOffset, nsec
}

// arosFindUTCOffset returns UTC minus AROS's clock in seconds (go/HANDOFF.md
// D21): from the host on hosted AROS, else from the locale.
func arosFindUTCOffset() int64 {
	if off, ok := arosHostUTCOffset(); ok {
		return off
	}
	return int64(arosLocaleGMTOffset()) * 60
}

// arosHostUTCOffset asks the host's C library for UTC through hostlib.resource,
// which only hosted AROS has, as the hosted battclock does
// (arch/all-unix/battclock/battclock_init.c). AROS's clock is set in whole
// seconds and lags the host by a second or two (F19), so the difference is
// rounded to a minute: Go's time then agrees with AROS's own (file dates).
func arosHostUTCOffset() (int64, bool) {
	hl := aroscall(arosvec(arosSysBase, _LVO_OpenResource), cstr("hostlib.resource\x00"), arosSysBase, 0, 0, 0, 0)
	if hl == 0 {
		return 0, false
	}
	var libc uintptr
	for _, name := range [...]string{"libSystem.dylib\x00", "libc.so.6\x00", "libc.so\x00"} {
		if libc = aroscall(arosvec(hl, _LVO_HostLib_Open), cstr(name), 0, hl, 0, 0, 0); libc != 0 {
			break
		}
	}
	if libc == 0 {
		return 0, false
	}
	var off int64
	hosttime := aroscall(arosvec(hl, _LVO_HostLib_GetPointer), libc, cstr("time\x00"), 0, hl, 0, 0)
	if hosttime != 0 {
		aroscall(arosvec(hl, _LVO_HostLib_Lock), hl, 0, 0, 0, 0, 0)
		utc := int64(aroscall(hosttime, 0, 0, 0, 0, 0, 0)) // host time(NULL)
		aroscall(arosvec(hl, _LVO_HostLib_Unlock), hl, 0, 0, 0, 0, 0)
		local, _ := arosLocalTime()
		if d := utc - local; d >= 0 {
			off = (d + 30) / 60 * 60
		} else {
			off = -((-d + 30) / 60 * 60)
		}
	}
	aroscall(arosvec(hl, _LVO_HostLib_Close), libc, 0, hl, 0, 0, 0)
	return off, hosttime != 0
}

// arosLocaleGMTOffset returns the locale's loc_GMTOffset: minutes from AROS's
// clock to UTC, AmigaOS sign (minutes west; workbench/c/SetClock.c). 0 if
// locale.library doesn't open. AROS's locale has no DST (F19).
func arosLocaleGMTOffset() int32 {
	lb := aroscall(arosvec(arosSysBase, _LVO_OpenLibrary), cstr("locale.library\x00"), 0, arosSysBase, 0, 0, 0)
	if lb == 0 {
		return 0
	}
	var off int32
	if loc := aroscall(arosvec(lb, _LVO_OpenLocale), 0, lb, 0, 0, 0, 0); loc != 0 {
		off = *(*int32)(unsafe.Pointer(loc + _Locale_loc_GMTOffset))
		aroscall(arosvec(lb, _LVO_CloseLocale), loc, lb, 0, 0, 0, 0)
	}
	aroscall(arosvec(arosSysBase, _LVO_CloseLibrary), lb, arosSysBase, 0, 0, 0, 0)
	return off
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

// The bridge for package syscall (milestone 2b). Each call is a system call
// on g0, as every AROS call is (go/HANDOFF.md D11); IoErr is read in the
// same one, so no other goroutine's dos call can come between.
//
//go:linkname syscall_aroscall syscall.aroscall
//go:nosplit
//go:uintptrkeepalive
func syscall_aroscall(fn, a0, a1, a2, a3, a4, a5 uintptr) (r, ioerr uintptr) {
	entersyscall()
	r = aroscall(fn, a0, a1, a2, a3, a4, a5)
	ioerr = aroscall(arosvec(arosDOSBase, _LVO_IoErr), arosDOSBase, 0, 0, 0, 0, 0)
	exitsyscall()
	return
}

//go:linkname syscall_arosBases syscall.arosBases
func syscall_arosBases() (sysBase, dosBase uintptr) { return arosSysBase, arosDOSBase }

// syscall_arosShellDir is the shell's current directory: syscall.Chdir
// must not unlock it (arosLeave gives it back).
//
//go:linkname syscall_arosShellDir syscall.arosShellDir
func syscall_arosShellDir() uintptr { return arosShellDir }

// time_arosLocalOffset gives package time the zone offset in seconds east
// of UTC for Local's fixed zone (go/HANDOFF.md D20, D21).
//
//go:linkname time_arosLocalOffset time.arosLocalOffset
func time_arosLocalOffset() int { return int(-arosUTCOffset) }
