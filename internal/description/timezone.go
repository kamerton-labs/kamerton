// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package description

import (
	"errors"
	"fmt"
	"strings"
	"time"
	// Embedded zone database: names resolve the same way in any image, with or
	// without /usr/share/zoneinfo.
	_ "time/tzdata"
)

// LoadTimeZone loads spec.notifications.timeZone, an IANA name; "" is UTC.
func LoadTimeZone(name string) (*time.Location, error) {
	switch {
	case name == "":
		return time.UTC, nil
	case name == "Local":
		// time.LoadLocation would return the zone of the machine Kamerton
		// runs on, which is not what a description means.
		return nil, errors.New(`"Local" is not a time zone name; use an IANA name such as Asia/Almaty`)
	case strings.TrimSpace(name) != name:
		return nil, fmt.Errorf("time zone %q has spaces around the name", name)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("unknown time zone %q; use an IANA name such as Asia/Almaty or Europe/Berlin", name)
	}
	return loc, nil
}
