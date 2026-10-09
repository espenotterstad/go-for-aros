// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

#include "go_asm.h"
#include "textflag.h"

// AROS's C startup (startup.o) calls main(argc, argv) on the shell's stack
// (40 KB, no guard page). main saves the C state, widens the task's stack
// bounds to 0..~0 (goroutine stacks live in the heap; the scheduler suspends a
// task whose SP leaves its bounds), gives Go a 128 KB g0 stack and enters
// rt0_go. It also points the task's trap code at arosTrap. runtime·exit
// (sys_aros_arm64.s) undoes it all and returns the exit code
// to the C startup. See go/HANDOFF.md D8, D9, D11.

// The linker's entry symbol; the C startup calls main directly.
TEXT _rt0_arm64_aros(SB),NOSPLIT|NOFRAME,$0
	B	main(SB)

TEXT main(SB),NOSPLIT|NOFRAME|TOPFRAME,$0
	MOVD	$runtime·arosCSave(SB), R9
	STP	(R19, R20), 0(R9)
	STP	(R21, R22), 16(R9)
	STP	(R23, R24), 32(R9)
	STP	(R25, R26), 48(R9)
	STP	(R27, g), 64(R9)		// R28 is g in Go assembly
	STP	(R29, R30), 80(R9)
	FSTPD	(F8, F9), 96(R9)
	FSTPD	(F10, F11), 112(R9)
	FSTPD	(F12, F13), 128(R9)
	FSTPD	(F14, F15), 144(R9)
	MOVD	RSP, R10
	MOVD	R10, 160(R9)
	MOVD	R0, R19			// argc
	MOVD	R1, R20			// argv

	MOVD	$SysBase(SB), R9
	MOVD	(R9), R21		// R21 = SysBase
	MOVD	R21, runtime·arosSysBase(SB)
	MOVD	$DOSBase(SB), R9
	MOVD	(R9), R10
	MOVD	R10, runtime·arosDOSBase(SB)

	// task = FindTask(NULL); save its stack bounds, then widen them.
	MOVD	$0, R0
	MOVD	R21, R1
	MOVD	-(8*const__LVO_FindTask)(R21), R9
	BL	(R9)
	MOVD	R0, runtime·arosTask(SB)
	MOVD	$runtime·arosCSave(SB), R9
	MOVD	const__Task_tc_SPLower(R0), R10
	MOVD	R10, 168(R9)
	MOVD	const__Task_tc_SPUpper(R0), R10
	MOVD	R10, 176(R9)
	MOVD	ZR, const__Task_tc_SPLower(R0)
	MOVD	$-1, R10
	MOVD	R10, const__Task_tc_SPUpper(R0)

	// Faults go to arosTrap (sys_aros_arm64.s) until arosLeave restores the
	// task's trap code.
	MOVD	const__Task_tc_TrapCode(R0), R10
	MOVD	R10, runtime·arosOldTrap(SB)
	MOVD	$runtime·arosTrap(SB), R10
	MOVD	R10, const__Task_tc_TrapCode(R0)

	// Shell state Go borrows (D18): pr_WindowPtr -1 keeps "insert volume"
	// requesters away; the current directory is saved for arosLeave. And
	// TF_STACKCHK off, since the bounds are 0..~0 (D9).
	MOVD	const__Process_pr_CurrentDir(R0), R10
	MOVD	R10, runtime·arosShellDir(SB)
	MOVD	const__Process_pr_WindowPtr(R0), R10
	MOVD	R10, runtime·arosShellWindowPtr(SB)
	MOVD	$-1, R10
	MOVD	R10, const__Process_pr_WindowPtr(R0)
	MOVBU	const__Task_tc_Flags(R0), R10
	MOVB	R10, runtime·arosShellTaskFlags(SB)
	MOVD	$const__TF_STACKCHK, R11
	BIC	R11, R10
	MOVB	R10, const__Task_tc_Flags(R0)

	// g0 stack = AllocMem(arosG0StackSize, MEMF_ANY)
	MOVD	$const_arosG0StackSize, R0
	MOVD	$const__MEMF_ANY, R1
	MOVD	R21, R2
	MOVD	-(8*const__LVO_AllocMem)(R21), R9
	BL	(R9)
	CBZ	R0, nomem
	MOVD	R0, runtime·arosG0Stack(SB)
	ADD	$const_arosG0StackSize, R0, R10
	MOVD	R10, RSP		// rt0_go takes the top 64 KB as g0's stack; C calls may use the rest

	MOVD	R19, R0
	MOVD	R20, R1
	B	runtime·rt0_go(SB)	// never returns; runtime·exit leaves through arosLeave

nomem:
	// No g0 stack: say so on the error stream (pr_CES, else Output()).
	MOVD	runtime·arosTask(SB), R9
	MOVD	const__Process_pr_CES(R9), R0
	CBNZ	R0, nomemfh
	MOVD	runtime·arosDOSBase(SB), R0
	MOVD	-(8*const__LVO_Output)(R0), R9
	BL	(R9)
nomemfh:
	MOVD	$nomemmsg<>(SB), R1
	MOVD	$44, R2
	MOVD	runtime·arosDOSBase(SB), R3
	MOVD	-(8*const__LVO_Write)(R3), R9
	BL	(R9)
	MOVD	$20, R0			// RETURN_FAIL
	B	runtime·arosLeave(SB)

DATA	nomemmsg<>+0(SB)/8, $"runtime:"
DATA	nomemmsg<>+8(SB)/8, $" aros: c"
DATA	nomemmsg<>+16(SB)/8, $"annot al"
DATA	nomemmsg<>+24(SB)/8, $"locate t"
DATA	nomemmsg<>+32(SB)/8, $"he g0 st"
DATA	nomemmsg<>+40(SB)/4, $"ack\n"
GLOBL	nomemmsg<>(SB), RODATA, $44
