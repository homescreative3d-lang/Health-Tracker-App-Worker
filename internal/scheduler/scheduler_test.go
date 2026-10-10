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
