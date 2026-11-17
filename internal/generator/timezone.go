// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package generator

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kamerton-labs/kamerton/internal/description"
)

// Times in messages use spec.notifications.timeZone, as "09:18 16.11 UTC+5". Kamerton
// computes offsets itself: Alertmanager's tz fails for names its database lacks (v0.35.0 has
// no Asia/Calcutta), which aborts the whole notification. Offsets cover zoneHistory before
// generation to zoneHorizon after it; later times keep the last known offset.
const (
	zoneHistory = 366 * 24 * time.Hour // resolved alerts may have started long ago
	zoneHorizon = 3 * 366 * 24 * time.Hour
	// zoneTimeLayout has no zone: the label is appended separately, since digits in
	// a layout are formatting directives.
	zoneTimeLayout = "15:04 02.01"
)

// zoneSegment starts at from, in Unix seconds.
type zoneSegment struct {
	from   int64
	offset int // seconds east of UTC
}

type zoneClock struct {
	segments []zoneSegment // ascending; the first applies to all earlier times
}

// newZoneClock falls back to UTC for "" or an unknown name.
func newZoneClock(name string, now time.Time) zoneClock {
	loc, err := description.LoadTimeZone(name)
	if err != nil || loc == nil {
		loc = time.UTC
	}
	start, end := now.Add(-zoneHistory), now.Add(zoneHorizon)
	_, off := start.In(loc).Zone()
	c := zoneClock{segments: []zoneSegment{{from: start.Unix(), offset: off}}}
	for t := start; ; {
		_, next := t.In(loc).ZoneBounds()
		if next.IsZero() || !next.After(t) || next.After(end) {
			break
		}
		if _, o := next.In(loc).Zone(); o != c.segments[len(c.segments)-1].offset {
			c.segments = append(c.segments, zoneSegment{from: next.Unix(), offset: o})
		}
		t = next
	}
	return c
}

// OffsetLabel renders a UTC offset in seconds as "UTC", "UTC+5", "UTC-3",
// "UTC+5:30" or "UTC+5:45".
func OffsetLabel(seconds int) string {
	if seconds == 0 {
		return "UTC"
	}
	sign := "+"
	if seconds < 0 {
		sign, seconds = "-", -seconds
	}
	h, m := seconds/3600, seconds%3600/60
	if m == 0 {
		return fmt.Sprintf("UTC%s%d", sign, h)
	}
	return fmt.Sprintf("UTC%s%d:%02d", sign, h, m)
}

func (c zoneClock) assign(variable, timeExpr string) string {
	format := func(s zoneSegment) string {
		t := timeExpr + ".UTC"
		if s.offset != 0 {
			t = "(" + timeExpr + ".UTC.Add " + strconv.FormatInt(int64(s.offset)*int64(time.Second), 10) + ")"
		}
		return fmt.Sprintf(`printf "%%s %s" (%s.Format %q)`, OffsetLabel(s.offset), t, zoneTimeLayout)
	}
	var b strings.Builder
	for i, s := range c.segments {
		if i == 0 {
			b.WriteString("{{ " + variable + " := " + format(s) + " }}")
			continue
		}
		b.WriteString("{{ if ge " + timeExpr + ".Unix " + strconv.FormatInt(s.from, 10) + " }}{{ " + variable + " = " + format(s) + " }}{{ end }}")
	}
	return b.String()
}
