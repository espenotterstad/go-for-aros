// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ld

import (
	"os/exec"
	"path/filepath"
)

// hostlinkAros links go.o with AROS's C driver (go/HANDOFF.md D8): the driver
// adds AROS's startup code and libraries and writes the relocatable ELF that
// LoadSeg loads. None of hostlink's ELF executable flags (-rdynamic, -pie,
// --build-id, -z ...) apply.
func (ctxt *Link) hostlinkAros() {
	argv := append([]string{}, ctxt.extld()...)
	argv = append(argv, "-o", *flagOutfile, filepath.Join(*flagTmpdir, "go.o"))
	argv = append(argv, ctxt.hostobjCopy()...)
	argv = append(argv, flagExtldflags...)
	if ctxt.Debugvlog != 0 {
		ctxt.Logf("host link: %q\n", argv)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		hint := ""
		if filepath.Base(argv[0]) != "aros-cc" {
			// Without CC, cmd/go hands the host's C compiler an AArch64 ELF object.
			hint = "\nGOOS=aros links with AROS's C driver: set CC=$GOROOT/misc/aros/aros-cc and AROS_BIN to an AROS build's bin/<target> directory"
		}
		Exitf("running %s failed: %v\n%s\n%s%s", argv[0], err, cmd, out, hint)
	}
	if len(out) > 0 {
		ctxt.Logf("%s", out)
	}
}
