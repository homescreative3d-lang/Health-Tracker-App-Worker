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


func TestMissedDoseTimeIsOneHourAfterSchedule(t *testing.T) {
	scheduled := time.Date(2026, 10, 11, 20, 0, 0, 0, time.UTC)
	if got, want := missedDoseTime(scheduled), scheduled.Add(time.Hour); !got.Equal(want) {
		t.Fatalf("missedDoseTime = %s, want %s", got, want)
	}
}

func TestFinalAlertCatchUpWindow(t *testing.T) {
	now := time.Date(2026, 10, 11, 20, 30, 0, 0, time.UTC)
	cases := []struct {
		name string
		scheduled time.Time
		want bool
	}{
		{name: "future dose", scheduled: now.Add(30 * time.Minute), want: true},
		{name: "dose 30 minutes ago", scheduled: now.Add(-30 * time.Minute), want: true},
		{name: "dose exactly one hour ago", scheduled: now.Add(-time.Hour), want: false},
		{name: "dose more than one hour ago", scheduled: now.Add(-2 * time.Hour), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldScheduleFinal(tc.scheduled, now); got != tc.want {
				t.Fatalf("shouldScheduleFinal = %v, want %v", got, tc.want)
			}
		})
	}
}
