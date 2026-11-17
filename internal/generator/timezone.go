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

// Times in notification messages are shown in the time zone of the
// description (spec.notifications.timeZone), with the UTC offset as label:
// "09:18 16.11 UTC+5".
//
// Alertmanager v0.27+ has a tz template function, and the official image
// carries a zoneinfo database, but tz fails for any name its database lacks
// (the v0.35.0 image has only canonical names: no Asia/Calcutta, Europe/Kiev,
// US/Eastern), and a failing function aborts the whole template, so the
// notification would not be sent at all. Kamerton cannot see which database
// an Alertmanager has (it may be a different image or version). So Kamerton
// resolves the zone itself, with Go's time-zone database (embedded in the
// binary, see package description), and the template only adds the offset to
// the UTC time with time.Time.Add, which every Alertmanager can do.
//
// Daylight saving: the offsets and the moments they change are computed for
// the period from zoneHistory before generation to zoneHorizon after it; the
// template picks the offset by the Unix time of each timestamp. Configuration
// is regenerated whenever Kamerton reconciles (start, description change), so
// the horizon only has to outlast the time between upgrades; after it the last
// known offset stays in use.
const (
	zoneHistory = 366 * 24 * time.Hour // resolved alerts may have started long ago
	zoneHorizon = 3 * 366 * 24 * time.Hour
	// zoneTimeLayout formats the time of day and the date, without the zone:
	// the label is appended separately, since digits in a layout are
	// formatting directives.
	zoneTimeLayout = "15:04 02.01"
)

// zoneSegment is a period with one UTC offset, starting at from (Unix seconds).
type zoneSegment struct {
	from   int64
	offset int // seconds east of UTC
}

// zoneClock renders timestamps of one time zone in Alertmanager templates.
type zoneClock struct {
	segments []zoneSegment // ascending; the first applies to all earlier times
}

// newZoneClock returns the clock of a zone name ("" or an unknown name: UTC)
// for configuration generated at now.
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

// assign returns template actions that set the variable (for example
// "$since") to the time expression (for example "$a.StartsAt") formatted in
// the zone, with the offset label.
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
