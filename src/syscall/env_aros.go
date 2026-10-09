// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The environment (go/HANDOFF.md D19): AROS variables are read live through
// GetVar (local, then global); Setenv, Unsetenv and Clearenv change only a
// Go-side overlay, never the shell's variables. Names fold case, as AROS's
// do.

package syscall

import (
	"sync"
	"unsafe"
)

var (
	envLock    sync.RWMutex
	envOverlay map[string]envEntry // by folded name
	envCleared bool                // Clearenv: AROS variables no longer show
)

type envEntry struct {
	key, value string
	unset      bool
}

func envFold(s string) string {
	for i := 0; i < len(s); i++ {
		if 'a' <= s[i] && s[i] <= 'z' {
			b := []byte(s)
			for j := i; j < len(b); j++ {
				if 'a' <= b[j] && b[j] <= 'z' {
					b[j] -= 'a' - 'A'
				}
			}
			return string(b)
		}
	}
	return s
}

// getVar reads AROS variable key with GetVar flags, growing the buffer to
// the variable's length (IoErr after a GetVar that fits or is cut).
func getVar(key string, flags uintptr) (string, bool) {
	k, err := BytePtrFromString(key)
	if err != nil {
		return "", false
	}
	for n := 256; ; {
		buf := make([]byte, n)
		r, e := dosCall(_LVO_GetVar, uintptr(unsafe.Pointer(k)), uintptr(unsafe.Pointer(&buf[0])), uintptr(n), flags, dosBase)
		got := int(int32(r))
		if got < 0 {
			return "", false
		}
		if int(e) < n || n >= 1<<20 {
			return string(buf[:got]), true
		}
		n = int(e) + 1
	}
}

func Getenv(key string) (value string, found bool) {
	if len(key) == 0 {
		return "", false
	}
	envLock.RLock()
	ent, ok := envOverlay[envFold(key)]
	cleared := envCleared
	envLock.RUnlock()
	if ok {
		return ent.value, !ent.unset
	}
	if cleared {
		return "", false
	}
	return getVar(key, 0)
}

func Setenv(key, value string) error {
	if len(key) == 0 {
		return EINVAL
	}
	for i := 0; i < len(key); i++ {
		if key[i] == '=' || key[i] == 0 {
			return EINVAL
		}
	}
	for i := 0; i < len(value); i++ {
		if value[i] == 0 {
			return EINVAL
		}
	}
	envLock.Lock()
	if envOverlay == nil {
		envOverlay = make(map[string]envEntry)
	}
	envOverlay[envFold(key)] = envEntry{key: key, value: value}
	envLock.Unlock()
	runtimeSetenv(key, value)
	return nil
}

func Unsetenv(key string) error {
	envLock.Lock()
	if envOverlay == nil {
		envOverlay = make(map[string]envEntry)
	}
	envOverlay[envFold(key)] = envEntry{key: key, unset: true}
	envLock.Unlock()
	runtimeUnsetenv(key)
	return nil
}

func Clearenv() {
	env := Environ()
	envLock.Lock()
	envOverlay = make(map[string]envEntry)
	envCleared = true
	envLock.Unlock()
	m := make(map[string]int)
	for _, kv := range env {
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				m[kv[:i]] = 0
				break
			}
		}
	}
	runtimeClearenv(m)
}

// Environ returns the process's local variables, then the top-level ENV:
// variables whose value has no NUL, then the overlay; each name once.
func Environ() []string {
	envLock.RLock()
	cleared := envCleared
	overlay := make(map[string]envEntry, len(envOverlay))
	for k, v := range envOverlay {
		overlay[k] = v
	}
	envLock.RUnlock()

	var env []string
	seen := make(map[string]bool)
	add := func(key, value string) {
		f := envFold(key)
		if seen[f] {
			return
		}
		seen[f] = true
		if ent, ok := overlay[f]; ok {
			if ent.unset {
				return
			}
			key, value = ent.key, ent.value
		}
		env = append(env, key+"="+value)
	}
	if !cleared {
		for _, key := range localVarNames() {
			if v, ok := getVar(key, _GVF_LOCAL_ONLY); ok {
				add(key, v)
			}
		}
		for _, key := range globalVarNames() {
			// The value as Getenv reads it (up to a newline), from files
			// that hold text.
			if b, ok := getVar(key, _GVF_GLOBAL_ONLY|_GVF_BINARY_VAR); ok && !hasNUL(b) {
				if v, ok := getVar(key, _GVF_GLOBAL_ONLY); ok {
					add(key, v)
				}
			}
		}
	}
	for _, ent := range overlay {
		if !ent.unset {
			add(ent.key, ent.value)
		}
	}
	return env
}

func hasNUL(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == 0 {
			return true
		}
	}
	return false
}

// localVarNames lists the process's local variables (pr_LocalVars, LV_VAR
// entries not marked LVF_IGNORE). The list belongs to this process, and
// only this process's own SetVar calls change it.
func localVarNames() []string {
	t, _ := aroscall(*(*uintptr)(unsafe.Pointer(sysBase - 8*_LVO_FindTask)), 0, sysBase, 0, 0, 0, 0)
	var names []string
	head := t + _Process_pr_LocalVars
	for n := *(*uintptr)(unsafe.Pointer(head)); n != 0; n = *(*uintptr)(unsafe.Pointer(n + _Node_ln_Succ)) {
		if *(*uintptr)(unsafe.Pointer(n + _Node_ln_Succ)) == 0 {
			break // the list's tail node
		}
		if *(*uint8)(unsafe.Pointer(n + _Node_ln_Type)) != _LV_VAR ||
			*(*uint16)(unsafe.Pointer(n + _LocalVar_lv_Flags))&_LVF_IGNORE != 0 {
			continue
		}
		names = append(names, gostring(*(*uintptr)(unsafe.Pointer(n + _Node_ln_Name))))
	}
	return names
}

// gostring copies the C string at p.
func gostring(p uintptr) string {
	if p == 0 {
		return ""
	}
	n := 0
	for *(*byte)(unsafe.Pointer(p + uintptr(n))) != 0 {
		n++
	}
	return string(unsafe.Slice((*byte)(unsafe.Pointer(p)), n))
}

// globalVarNames lists the files at the top of ENV:.
func globalVarNames() []string {
	lock, err := Lock("ENV:", LockShared)
	if err != nil {
		return nil
	}
	defer UnLock(lock)
	var names []string
	fib := new(FileInfoBlock)
	if Examine(lock, fib) != nil {
		return nil
	}
	for ExNext(lock, fib) == nil {
		if fib.DirEntryType < 0 {
			names = append(names, fib.Name())
		}
	}
	return names
}
