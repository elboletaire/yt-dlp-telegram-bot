package main

import (
	"testing"
	"time"
)

func mustSchedule(t *testing.T, interval, at string) Schedule {
	t.Helper()
	s, err := ParseSchedule(interval, at)
	if err != nil {
		t.Fatalf("ParseSchedule(%q, %q): %v", interval, at, err)
	}
	return s
}

func TestParseSchedule(t *testing.T) {
	cases := []struct {
		interval, at string
		want         Schedule
	}{
		{"day", "08:30", Schedule{Interval: IntervalDay, Hour: 8, Minute: 30}},
		{"day", "", Schedule{Interval: IntervalDay, Hour: 20}},
		{"week", "", Schedule{Interval: IntervalWeek, Weekday: time.Sunday, Hour: 20}},
		{"WEEK", "Fri 18:05", Schedule{Interval: IntervalWeek, Weekday: time.Friday, Hour: 18, Minute: 5}},
		{"month", "", Schedule{Interval: IntervalMonth, Day: 1, Hour: 20}},
		{"month", "28 00:00", Schedule{Interval: IntervalMonth, Day: 28}},
		{"off", "whatever", Schedule{Interval: IntervalOff}},
	}
	for _, c := range cases {
		if got := mustSchedule(t, c.interval, c.at); got != c.want {
			t.Errorf("ParseSchedule(%q, %q) = %+v, want %+v", c.interval, c.at, got, c.want)
		}
	}
}

func TestParseScheduleErrors(t *testing.T) {
	bad := [][2]string{
		{"year", ""},
		{"day", "25:00"},
		{"day", "12:60"},
		{"day", "sun 20:00"},
		{"week", "20:00"},
		{"week", "sunday 20:00"},
		{"month", "29 20:00"},
		{"month", "0 20:00"},
		{"month", "sun 20:00"},
	}
	for _, b := range bad {
		if _, err := ParseSchedule(b[0], b[1]); err == nil {
			t.Errorf("ParseSchedule(%q, %q) expected error", b[0], b[1])
		}
	}
}

func TestDaySchedule(t *testing.T) {
	s := mustSchedule(t, "day", "20:00")
	at := time.Date(2026, 10, 10, 19, 0, 0, 0, time.UTC)
	if got, want := s.Next(at), time.Date(2026, 10, 10, 20, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("Next = %v, want %v", got, want)
	}
	if got, want := s.Prev(at), time.Date(2026, 10, 9, 20, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("Prev = %v, want %v", got, want)
	}
	// Exactly on an occurrence: Next is the following one, Prev the previous one.
	on := time.Date(2026, 10, 10, 20, 0, 0, 0, time.UTC)
	if got := s.Next(on); !got.Equal(on.AddDate(0, 0, 1)) {
		t.Errorf("Next(on) = %v", got)
	}
	if got := s.Prev(on); !got.Equal(on.AddDate(0, 0, -1)) {
		t.Errorf("Prev(on) = %v", got)
	}
}

func TestWeekSchedule(t *testing.T) {
	s := mustSchedule(t, "week", "sun 20:00")
	sat := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC) // Saturday
	if got, want := s.Next(sat), time.Date(2026, 10, 11, 20, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("Next = %v, want %v", got, want)
	}
	if got, want := s.Prev(sat), time.Date(2026, 10, 4, 20, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("Prev = %v, want %v", got, want)
	}
	sunLate := time.Date(2026, 10, 11, 21, 0, 0, 0, time.UTC)
	if got, want := s.Next(sunLate), time.Date(2026, 10, 18, 20, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("Next(sunLate) = %v, want %v", got, want)
	}
	if got, want := s.Prev(sunLate), time.Date(2026, 10, 11, 20, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("Prev(sunLate) = %v, want %v", got, want)
	}
}

func TestMonthSchedule(t *testing.T) {
	s := mustSchedule(t, "month", "1 20:00")
	at := time.Date(2026, 12, 15, 0, 0, 0, 0, time.UTC)
	if got, want := s.Next(at), time.Date(2027, 1, 1, 20, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("Next = %v, want %v (year wrap)", got, want)
	}
	if got, want := s.Prev(at), time.Date(2026, 12, 1, 20, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("Prev = %v, want %v", got, want)
	}
	early := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	if got, want := s.Prev(early), time.Date(2025, 12, 1, 20, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("Prev(early) = %v, want %v (year wrap back)", got, want)
	}
}

func TestScheduleDST(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		t.Skip("tzdata not available:", err)
	}
	s := mustSchedule(t, "day", "20:00")
	// DST ends on 2026-10-25 in Europe/Madrid; occurrences stay at 20:00 local time.
	at := time.Date(2026, 10, 25, 21, 0, 0, 0, loc)
	if got, want := s.Prev(at), time.Date(2026, 10, 25, 20, 0, 0, 0, loc); !got.Equal(want) {
		t.Errorf("Prev = %v, want %v", got, want)
	}
	if got, want := s.Next(at), time.Date(2026, 10, 26, 20, 0, 0, 0, loc); !got.Equal(want) {
		t.Errorf("Next = %v, want %v", got, want)
	}
	if got := s.Next(at).Sub(s.Prev(at)); got != 24*time.Hour {
		t.Errorf("interval across DST = %v (local times must be used)", got)
	}
	before := time.Date(2026, 10, 25, 10, 0, 0, 0, loc)
	if got, want := s.Next(before).Sub(s.Prev(before)), 25*time.Hour; got != want {
		t.Errorf("DST day length = %v, want %v", got, want)
	}
}
