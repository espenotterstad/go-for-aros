// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package time

import (
	"internal/strconv"
	"syscall"
)

// AROS ships no IANA zoneinfo: zones load from the time/tzdata package or
// $GOROOT/lib/time/zoneinfo.zip, and LoadLocation also reads ZONEINFO.
var platformZoneSources []string

// arosLocalOffset is the runtime's UTC offset in seconds east: from the
// host on hosted AROS, from the locale on native (go/HANDOFF.md D21).
func arosLocalOffset() int

// initLocal sets Local (go/HANDOFF.md D20): the IANA zone TZ names, if it
// loads; UTC for TZ="" or "UTC"; else a fixed zone at the runtime's offset,
// named as js names it ("UTC+2", "UTC-3:30").
func initLocal() {
	tz, ok := syscall.Getenv("TZ")
	if ok && tz != "" && tz[0] == ':' {
		tz = tz[1:]
	}
	switch {
	case ok && (tz == "" || tz == "UTC"):
		localLoc.name = "UTC"
		return
	case ok:
		if z, err := loadLocation(tz, platformZoneSources); err == nil {
			localLoc = *z
			return
		}
	}
	localLoc.name = "Local"
	offset := arosLocalOffset()
	z := zone{name: "UTC", offset: offset}
	if offset < 0 {
		z.name += "-"
		offset = -offset
	} else {
		z.name += "+"
	}
	z.name += strconv.Itoa(offset / 3600)
	if min := offset / 60 % 60; min != 0 {
		z.name += ":" + strconv.Itoa(min)
	}
	localLoc.zone = []zone{z}
}
