// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

#include "go_asm.h"
#include "textflag.h"

// aroscall_trampoline runs on g0 (via asmcgocall) with R0 = *aroscallArgs
// {fn, a[6], ret}: it calls fn(a0..a5) with the C ABI. An AROS library
// function takes its library base as its last argument (go/HANDOFF.md D11).
TEXT runtime·aroscall_trampoline(SB),NOSPLIT,$16
	MOVD	R0, 8(RSP)
	MOVD	0(R0), R9
	MOVD	16(R0), R1
	MOVD	24(R0), R2
	MOVD	32(R0), R3
	MOVD	40(R0), R4
	MOVD	48(R0), R5
	MOVD	8(R0), R0
	BL	(R9)
	MOVD	8(RSP), R1
	MOVD	R0, 56(R1)
	RET

TEXT runtime·exit(SB),NOSPLIT|NOFRAME,$0-4
	MOVW	code+0(FP), R0
	B	runtime·arosLeave(SB)

// arosLeave returns R0 (the exit code) to the C startup: back on the shell's
// stack it gives back m0's timer port, request and semaphore signal, closes
// timer.device, frees the heap and the g0 stack, restores what main changed
// in the task (stack bounds, trap code, TF_STACKCHK, pr_WindowPtr,
// pr_CurrentDir) and the C registers saved by main.
TEXT runtime·arosLeave(SB),NOSPLIT|NOFRAME,$0
	MOVW	R0, R19
	MOVD	$runtime·arosCSave(SB), R20
	MOVD	160(R20), R10
	MOVD	R10, RSP
	MOVD	runtime·arosSysBase(SB), R21

	// m0's timed-wait request and port (minit) and semaphore signal
	// (semacreate) belong to the shell's process: give them back, with no
	// signal left pending. A request still at timer.device (a fault inside
	// semasleep or usleep) is aborted first.
	MOVD	$runtime·m0(SB), R22
	MOVD	(m_mOS+mOS_treq)(R22), R0
	CBZ	R0, notreq
	MOVWU	(m_mOS+mOS_tbusy)(R22), R10
	CBZ	R10, treqidle
	MOVD	R21, R1
	MOVD	-(8*const__LVO_AbortIO)(R21), R9
	BL	(R9)
	MOVD	(m_mOS+mOS_treq)(R22), R0
	MOVD	R21, R1
	MOVD	-(8*const__LVO_WaitIO)(R21), R9
	BL	(R9)
	MOVD	(m_mOS+mOS_treq)(R22), R0
treqidle:
	MOVD	R21, R1
	MOVD	-(8*const__LVO_DeleteIORequest)(R21), R9
	BL	(R9)
notreq:
	MOVD	(m_mOS+mOS_port)(R22), R23
	CBZ	R23, noport
	MOVBU	const__MsgPort_mp_SigBit(R23), R10
	MOVD	$1, R1
	LSL	R10, R1, R1
	MOVD	ZR, R0
	MOVD	R21, R2
	MOVD	-(8*const__LVO_SetSignal)(R21), R9
	BL	(R9)
	MOVD	R23, R0
	MOVD	R21, R1
	MOVD	-(8*const__LVO_DeleteMsgPort)(R21), R9
	BL	(R9)
noport:
	MOVW	(m_mOS+mOS_sigbit)(R22), R23
	CBZ	R23, nosem
	SUB	$1, R23
	MOVD	$1, R1
	LSL	R23, R1, R1
	MOVD	ZR, R0
	MOVD	R21, R2
	MOVD	-(8*const__LVO_SetSignal)(R21), R9
	BL	(R9)
	MOVD	R23, R0
	MOVD	R21, R1
	MOVD	-(8*const__LVO_FreeSignal)(R21), R9
	BL	(R9)
nosem:
	MOVWU	runtime·arosTimerOpen(SB), R10
	CBZ	R10, notimer
	MOVD	$runtime·arosTimerReq(SB), R0
	MOVD	R21, R1
	MOVD	-(8*const__LVO_CloseDevice)(R21), R9
	BL	(R9)
notimer:
	MOVD	runtime·arosHeapBase(SB), R0
	CBZ	R0, noheap
	MOVD	runtime·arosHeapSize(SB), R1
	MOVD	R21, R2
	MOVD	-(8*const__LVO_FreeMem)(R21), R9
	BL	(R9)
noheap:
	MOVD	runtime·arosG0Stack(SB), R0
	CBZ	R0, nog0
	MOVD	$const_arosG0StackSize, R1
	MOVD	R21, R2
	MOVD	-(8*const__LVO_FreeMem)(R21), R9
	BL	(R9)
nog0:
	MOVD	runtime·arosTask(SB), R22
	CBZ	R22, notask
	MOVD	168(R20), R10
	MOVD	R10, const__Task_tc_SPLower(R22)
	MOVD	176(R20), R10
	MOVD	R10, const__Task_tc_SPUpper(R22)
	MOVD	runtime·arosOldTrap(SB), R10
	MOVD	R10, const__Task_tc_TrapCode(R22)
	// TF_STACKCHK as main found it (D9).
	MOVBU	const__Task_tc_Flags(R22), R10
	MOVD	$const__TF_STACKCHK, R11
	BIC	R11, R10
	MOVBU	runtime·arosShellTaskFlags(SB), R12
	AND	R11, R12
	ORR	R12, R10
	MOVB	R10, const__Task_tc_Flags(R22)
	// The shell's requester setting and current directory (D18). Any other
	// current directory is a lock Go took (syscall.Chdir): unlock it.
	MOVD	runtime·arosShellWindowPtr(SB), R10
	MOVD	R10, const__Process_pr_WindowPtr(R22)
	MOVD	runtime·arosShellDir(SB), R0
	MOVD	const__Process_pr_CurrentDir(R22), R10
	CMP	R0, R10
	BEQ	notask
	MOVD	runtime·arosDOSBase(SB), R1
	MOVD	-(8*const__LVO_CurrentDir)(R1), R9
	BL	(R9)			// R0 = Go's lock
	MOVD	runtime·arosDOSBase(SB), R1
	MOVD	-(8*const__LVO_UnLock)(R1), R9
	BL	(R9)
notask:
	MOVW	R19, R0
	MOVD	R20, R9
	LDP	0(R9), (R19, R20)
	LDP	16(R9), (R21, R22)
	LDP	32(R9), (R23, R24)
	LDP	48(R9), (R25, R26)
	LDP	64(R9), (R27, g)
	LDP	80(R9), (R29, R30)
	FLDPD	96(R9), (F8, F9)
	FLDPD	112(R9), (F10, F11)
	FLDPD	128(R9), (F12, F13)
	FLDPD	144(R9), (F14, F15)
	RET

// arosTrap is the task's tc_TrapCode while Go runs (go/HANDOFF.md F1): exec
// calls it with the C ABI, R0 = trap number, R1 = struct ExceptionContext *,
// on the faulting task's stack below the host signal frame (F3).
//
// A fault in Go code, on a goroutine that may panic, becomes a call to
// sigpanic, set up as arm64's preparePanic does (signal_arm64.go): sp drops
// by 16 with lr at [sp] and fp at [sp-8], lr = the faulting pc, x28 (g)
// stays. Anything else resumes in arosFault, which reports it and exits; a
// fault while arosFault runs goes to the previous trap code (AROS's
// requester) instead of looping. Uses R0-R1 and R9-R14 only: no REGTMP (R27
// is callee-saved in C).
TEXT runtime·arosTrap(SB),NOSPLIT|NOFRAME,$0
	MOVD	$runtime·arosFaulted(SB), R9
	MOVWU	(R9), R10
	CBNZ	R10, again
	MOVD	const__ExceptionContext_pc(R1), R10	// R10 = faulting pc
	// Go code: text <= pc < etext, and not runtime·abort, which faults on purpose.
	MOVD	$runtime·text(SB), R9
	CMP	R9, R10
	BLO	fatal
	MOVD	$runtime·etext(SB), R9
	CMP	R9, R10
	BHS	fatal
	MOVD	$runtime·abort(SB), R9
	SUB	R9, R10, R11
	CMP	$16, R11
	BLO	fatal
	// g = x28: a goroutine in the heap (g0s are not), its m's curg, and not
	// in a stack split (signal_unix.go sighandler's condition). Checked in
	// that order, so nothing is read through a pointer that isn't a g.
	MOVD	const__ExceptionContext_x28(R1), R11
	MOVD	$runtime·arosHeapBase(SB), R9
	MOVD	(R9), R12
	CMP	R12, R11
	BLO	fatal
	MOVD	$runtime·arosHeapEnd(SB), R9
	MOVD	(R9), R12
	CMP	R12, R11
	BHS	fatal
	MOVD	g_m(R11), R12
	CBZ	R12, fatal
	MOVD	m_curg(R12), R12
	CMP	R11, R12
	BNE	fatal
	MOVBU	g_throwsplit(R11), R12
	CBNZ	R12, fatal
	// sigpanic's inputs (A8: no fault address).
	MOVW	R0, g_sig(R11)
	MOVD	R0, g_sigcode0(R11)
	MOVD	ZR, g_sigcode1(R11)
	MOVD	R10, g_sigpc(R11)
	// D12: how far below the goroutine's sp the host and exec frames reach.
	MOVD	const__ExceptionContext_sp(R1), R12
	MOVD	RSP, R13
	SUB	R13, R12, R13
	MOVD	$runtime·arosTrapDepth(SB), R9
	MOVD	(R9), R14
	CMP	R14, R13
	BLS	depthok
	MOVD	R13, (R9)
depthok:
	SUB	$16, R12
	MOVD	const__ExceptionContext_lr(R1), R13
	MOVD	R13, 0(R12)
	MOVD	const__ExceptionContext_fp(R1), R13
	MOVD	R13, -8(R12)
	MOVD	R12, const__ExceptionContext_sp(R1)
	MOVD	R10, const__ExceptionContext_lr(R1)
	MOVD	$runtime·sigpanic0(SB), R13
	MOVD	R13, const__ExceptionContext_pc(R1)
	RET
fatal:
	MOVD	$runtime·arosFaulted(SB), R9
	MOVW	$1, R10
	MOVW	R10, (R9)
	MOVD	$runtime·arosFaultCode(SB), R9
	MOVD	R0, (R9)
	MOVD	const__ExceptionContext_pc(R1), R10
	MOVD	$runtime·arosFaultPC(SB), R9
	MOVD	R10, (R9)
	MOVD	$runtime·arosFault(SB), R10
	MOVD	R10, const__ExceptionContext_pc(R1)
	RET
again:
	MOVD	$runtime·arosOldTrap(SB), R9
	MOVD	(R9), R9
	B	(R9)

// arosFault is where a fault arosTrap can't hand to sigpanic resumes. R28 may
// not be g (C code may have faulted), so take m0's g0 and the top of its
// stack, report the fault and leave with crashExitCode.
TEXT runtime·arosFault(SB),NOSPLIT|NOFRAME|TOPFRAME,$0
	MOVD	$runtime·g0(SB), g
	MOVD	(g_stack+stack_hi)(g), R10
	MOVD	R10, RSP
	MOVD	ZR, R29
	MOVD	ZR, R30
	BL	runtime·arosFatalFault(SB)
	MOVW	$const_crashExitCode, R0
	B	runtime·arosLeave(SB)
