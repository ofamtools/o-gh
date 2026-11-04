package main

import (
	"fmt"
	"time"
)

// On-call. The same setting names and rules as o-slack's (oncall.mode,
// oncall.start, oncall.weeks; ~/proj/o-slack slack.go, Oncall), copied
// rather than shared: their shared home is a later mtm profile. The one
// difference: oncall.start may be any weekday (a rota need not start on a
// Monday); the window is whole local days.

const (
	OncallAuto = "auto"
	OncallOn   = "on"
	OncallOff  = "off"
)

// Oncall is the on-call window. Start is the first day (YYYY-MM-DD, local
// time), Weeks how many weeks from there; Mode on or off overrides the
// dates.
type Oncall struct {
	Mode  string
	Start string
	Weeks int
}

// Window is the window's start and end (end excluded); false when no
// valid date is set.
func (o Oncall) Window(loc *time.Location) (time.Time, time.Time, bool) {
	if o.Start == "" {
		return time.Time{}, time.Time{}, false
	}
	start, err := time.ParseInLocation("2006-01-02", o.Start, loc)
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	weeks := o.Weeks
	if weeks <= 0 {
		weeks = 1
	}
	return start, start.AddDate(0, 0, 7*weeks), true
}

// Active reports whether you are on call at now (in now's location).
func (o Oncall) Active(now time.Time) bool {
	switch o.Mode {
	case OncallOn:
		return true
	case OncallOff:
		return false
	}
	start, end, ok := o.Window(now.Location())
	return ok && !now.Before(start) && now.Before(end)
}

// Problem is the oncall setting that cannot be used, or "".
func (o Oncall) Problem() string {
	if o.Start != "" {
		if _, err := time.ParseInLocation("2006-01-02", o.Start, time.UTC); err != nil {
			return fmt.Sprintf("oncall.start %q must be a date like 2026-10-19", o.Start)
		}
	}
	if o.Weeks < 0 || o.Weeks > 52 {
		return "oncall.weeks must be between 1 and 52"
	}
	return ""
}
