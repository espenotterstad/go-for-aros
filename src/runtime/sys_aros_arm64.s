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
// timer.device, frees the heap and the g0 stack, restores the task's stack
// bounds and trap code and the C registers saved by main.
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
	MOVD	runtime·arosTask(SB), R0
	CBZ	R0, notask
	MOVD	168(R20), R10
	MOVD	R10, const__Task_tc_SPLower(R0)
	MOVD	176(R20), R10
	MOVD	R10, const__Task_tc_SPUpper(R0)
	MOVD	runtime·arosOldTrap(SB), R10
	MOVD	R10, const__Task_tc_TrapCode(R0)
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
// calls it with the C ABI, R0 = trap number, R1 = struct ExceptionContext *.
// The first fault resumes the task in arosFault; a fault after that goes to
// the previous trap code (AROS's requester) instead of looping. Only R9 and
// R10 are used: no REGTMP (R27 is callee-saved in C).
TEXT runtime·arosTrap(SB),NOSPLIT|NOFRAME,$0
	MOVD	$runtime·arosFaulted(SB), R9
	MOVWU	(R9), R10
	CBNZ	R10, again
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

// arosFault is where a faulting task resumes. R28 may not be g (C code may
// have faulted), so take m0's g0 and the top of its stack, report the fault
// and leave with crashExitCode. Go's own fault handling (sigpanic) is milestone 2.
TEXT runtime·arosFault(SB),NOSPLIT|NOFRAME|TOPFRAME,$0
	MOVD	$runtime·g0(SB), g
	MOVD	(g_stack+stack_hi)(g), R10
	MOVD	R10, RSP
	MOVD	ZR, R29
	MOVD	ZR, R30
	BL	runtime·arosFatalFault(SB)
	MOVW	$const_crashExitCode, R0
	B	runtime·arosLeave(SB)
