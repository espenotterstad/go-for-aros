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
// stack it closes timer.device, frees the heap and the g0 stack, restores the
// task's stack bounds and the C registers saved by main.
TEXT runtime·arosLeave(SB),NOSPLIT|NOFRAME,$0
	MOVW	R0, R19
	MOVD	$runtime·arosCSave(SB), R20
	MOVD	160(R20), R10
	MOVD	R10, RSP
	MOVD	runtime·arosSysBase(SB), R21

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
