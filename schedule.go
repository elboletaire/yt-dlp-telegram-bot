package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Interval string

const (
	IntervalOff   Interval = "off"
	IntervalDay   Interval = "day"
	IntervalWeek  Interval = "week"
	IntervalMonth Interval = "month"
)

// Schedule describes when the best-of digest runs, in local time.
type Schedule struct {
	Interval Interval
	Weekday  time.Weekday // week only
	Day      int          // month only, 1-28
	Hour     int
	Minute   int
}

var scheduleWeekdays = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

var scheduleDefaultAt = map[Interval]string{
	IntervalDay:   "20:00",
	IntervalWeek:  "sun 20:00",
	IntervalMonth: "1 20:00",
}

func parseClock(s string) (hour, minute int, err error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid time %q, expected HH:MM", s)
	}
	return t.Hour(), t.Minute(), nil
}

// ParseSchedule parses an interval (day, week, month, off) and the time it runs
// at: "HH:MM" for day, "<mon..sun> HH:MM" for week, "<1-28> HH:MM" for month.
// An empty at uses the interval's default.
func ParseSchedule(interval, at string) (Schedule, error) {
	s := Schedule{Interval: Interval(strings.ToLower(strings.TrimSpace(interval)))}
	if s.Interval == IntervalOff {
		return Schedule{Interval: IntervalOff}, nil
	}
	def, ok := scheduleDefaultAt[s.Interval]
	if !ok {
		return s, fmt.Errorf("invalid best-of interval %q, expected day, week, month or off", interval)
	}
	at = strings.ToLower(strings.TrimSpace(at))
	if at == "" {
		at = def
	}

	fields := strings.Fields(at)
	clock := fields[len(fields)-1]
	switch {
	case s.Interval == IntervalDay && len(fields) == 1:
	case s.Interval == IntervalWeek && len(fields) == 2:
		wd, ok := scheduleWeekdays[fields[0]]
		if !ok {
			return s, fmt.Errorf("invalid best-of weekday %q, expected mon..sun", fields[0])
		}
		s.Weekday = wd
	case s.Interval == IntervalMonth && len(fields) == 2:
		day, err := strconv.Atoi(fields[0])
		if err != nil || day < 1 || day > 28 {
			return s, fmt.Errorf("invalid best-of day of month %q, expected 1-28", fields[0])
		}
		s.Day = day
	default:
		return s, fmt.Errorf("invalid best-of time %q for interval %s, expected e.g. %q", at, s.Interval, def)
	}

	var err error
	if s.Hour, s.Minute, err = parseClock(clock); err != nil {
		return s, err
	}
	return s, nil
}

// occurrence returns the occurrence in the period containing t (same day,
// same week starting on the schedule's weekday, or same month), shifted by
// offset periods.
func (s Schedule) occurrence(t time.Time, offset int) time.Time {
	y, m, d := t.Date()
	loc := t.Location()
	switch s.Interval {
	case IntervalWeek:
		d -= (int(t.Weekday()) - int(s.Weekday) + 7) % 7
		return time.Date(y, m, d+7*offset, s.Hour, s.Minute, 0, 0, loc)
	case IntervalMonth:
		return time.Date(y, m+time.Month(offset), s.Day, s.Hour, s.Minute, 0, 0, loc)
	default:
		return time.Date(y, m, d+offset, s.Hour, s.Minute, 0, 0, loc)
	}
}

// Next returns the first occurrence strictly after t.
func (s Schedule) Next(t time.Time) time.Time {
	o := s.occurrence(t, 0)
	if !o.After(t) {
		o = s.occurrence(t, 1)
	}
	return o
}

// Prev returns the last occurrence strictly before t.
func (s Schedule) Prev(t time.Time) time.Time {
	o := s.occurrence(t, 0)
	if !o.Before(t) {
		o = s.occurrence(t, -1)
	}
	return o
}
