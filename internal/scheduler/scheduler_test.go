package scheduler

import (
	"testing"
	"time"
	"database/sql"
)

func TestOccursDailyAndStartDate(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	m := medicine{Frequency:"daily", StartDate:start, DurationType:"ongoing", IsRecurring:true}
	if !occurs(m, start) { t.Fatal("medicine should occur on start date") }
	if !occurs(m, start.AddDate(0, 0, 1)) { t.Fatal("daily medicine should occur next day") }
	if occurs(m, start.AddDate(0, 0, -1)) { t.Fatal("medicine must not occur before start date") }
}

func TestOccursEveryOtherDay(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	m := medicine{Frequency:"everyOtherDay", StartDate:start, DurationType:"ongoing", IsRecurring:true}
	if !occurs(m, start) || occurs(m, start.AddDate(0, 0, 1)) || !occurs(m, start.AddDate(0, 0, 2)) {
		t.Fatal("everyOtherDay schedule is incorrect")
	}
}

func TestOccursPauseWindow(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	pauseStart := start.AddDate(0, 0, 2)
	pauseEnd := start.AddDate(0, 0, 4)
	m := medicine{Frequency:"daily", StartDate:start, DurationType:"ongoing", IsRecurring:true, PauseStart:sql.NullTime{Time: pauseStart, Valid: true}, PauseEnd:sql.NullTime{Time: pauseEnd, Valid: true}}
	if occurs(m, pauseStart) || occurs(m, pauseEnd) || !occurs(m, start.AddDate(0,0,5)) { t.Fatal("pause window handling is incorrect") }
}


func TestReminderOccurrenceHonorsRepeatInterval(t *testing.T) {
	scheduled := time.Date(2026, 10, 11, 20, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		now time.Time
		want time.Time
		ok bool
	}{
		{name: "first reminder", now: scheduled.Add(-15 * time.Minute), want: scheduled.Add(-15 * time.Minute), ok: true},
		{name: "repeat at ten minutes before", now: scheduled.Add(-9 * time.Minute), want: scheduled.Add(-10 * time.Minute), ok: true},
		{name: "repeat at five minutes before", now: scheduled.Add(-4 * time.Minute), want: scheduled.Add(-5 * time.Minute), ok: true},
		{name: "outside lead window", now: scheduled.Add(-16 * time.Minute), ok: false},
		{name: "dose already due", now: scheduled, ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := reminderOccurrence(scheduled, tc.now, 15, 5)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if tc.ok && !got.Equal(tc.want) {
				t.Fatalf("occurrence = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestFinalAndMissedNotificationWindows(t *testing.T) {
	cases := []struct {
		minutesUntil float64
		final bool
		missed bool
	}{
		{minutesUntil: 1, final: false, missed: false},
		{minutesUntil: 0, final: true, missed: false},
		{minutesUntil: -59.9, final: true, missed: false},
		{minutesUntil: -60, final: false, missed: true},
		{minutesUntil: -120, final: false, missed: true},
	}
	for _, tc := range cases {
		if got := finalNotificationDue(tc.minutesUntil); got != tc.final {
			t.Errorf("finalNotificationDue(%v) = %v, want %v", tc.minutesUntil, got, tc.final)
		}
		if got := missedNotificationDue(tc.minutesUntil); got != tc.missed {
			t.Errorf("missedNotificationDue(%v) = %v, want %v", tc.minutesUntil, got, tc.missed)
		}
	}
}
