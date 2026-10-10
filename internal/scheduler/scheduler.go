package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/homescreative3d-lang/Health-Tracker-App-Worker/internal/notifier"
)

type Scheduler struct {
	db       *sql.DB
	push     notifier.Sender
	fallback string
	log      *slog.Logger
}

func New(db *sql.DB, push notifier.Sender, fallback string, logger *slog.Logger) *Scheduler {
	return &Scheduler{db: db, push: push, fallback: fallback, log: logger}
}

func (s *Scheduler) Run(ctx context.Context, interval time.Duration) error {
	if err := s.Tick(ctx); err != nil {
		s.log.Error("initial scheduler tick failed", "error", err)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			start := time.Now()
			if err := s.Tick(ctx); err != nil {
				s.log.Error("scheduler tick failed", "error", err)
			} else {
				s.log.Info("scheduler tick completed", "duration_ms", time.Since(start).Milliseconds())
			}
		}
	}
}

type user struct {
	ID                 string
	TimeZone            string
	LeadMinutes         int
	RepeatMinutes       int
	FinalEnabled        bool
}
type medicine struct {
	ID, PatientID, Name, PatientName string
	Frequency, SpecificDays, Times   string
	CycleEvery                       int
	CycleUnit, DurationType, DurationUnit string
	DurationValue, Supply, RefillThreshold int
	StartDate                        time.Time
	IsRecurring                      bool
	PauseStart, PauseEnd             sql.NullTime
}

func (s *Scheduler) Tick(ctx context.Context) error {
	users, err := s.loadUsers(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, u := range users {
		if err := s.processUser(ctx, u); err != nil {
			s.log.Error("user scheduling failed", "user_id", u.ID, "error", err)
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (s *Scheduler) loadUsers(ctx context.Context) ([]user, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT "Id", "TimeZoneId", "NotificationLeadMinutes", "NotificationRepeatMinutes", "FinalNotificationEnabled" FROM "Users"`)
	if err != nil { return nil, fmt.Errorf("query users: %w", err) }
	defer rows.Close()
	var result []user
	for rows.Next() {
		var u user
		if err := rows.Scan(&u.ID, &u.TimeZone, &u.LeadMinutes, &u.RepeatMinutes, &u.FinalEnabled); err != nil { return nil, err }
		result = append(result, u)
	}
	return result, rows.Err()
}

func (s *Scheduler) processUser(ctx context.Context, u user) error {
	loc, err := time.LoadLocation(u.TimeZone)
	if err != nil { loc, _ = time.LoadLocation(s.fallback) }
	now := time.Now().In(loc)
	today := dateOnly(now)
	meds, err := s.loadMedicines(ctx, u.ID)
	if err != nil { return err }
	for _, m := range meds {
		if err := s.processMedicine(ctx, u, m, today, now, loc); err != nil {
			s.log.Error("medicine scheduling failed", "user_id", u.ID, "medicine_id", m.ID, "error", err)
		}
	}
	return nil
}

func (s *Scheduler) loadMedicines(ctx context.Context, userID string) ([]medicine, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT m."Id", m."PatientId", m."Name", p."Name",
       m."FrequencyPattern", m."SpecificDaysJson", m."TimesJson",
       m."CycleEvery", m."CycleUnit", m."DurationType", m."DurationValue",
       m."DurationUnit", m."StartDate", m."SupplyCount", m."RefillThreshold",
       m."IsRecurring", m."PauseStartDate", m."PauseEndDate"
FROM "Medicines" m
JOIN "Patients" p ON p."Id" = m."PatientId"
WHERE p."UserId" = $1
   OR p."UserId" IN (
      SELECT fm2."UserId" FROM "FamilyMembers" fm1
      JOIN "FamilyMembers" fm2 ON fm1."FamilyId" = fm2."FamilyId"
      WHERE fm1."UserId" = $1 AND fm1."Status" = 'approved' AND fm2."Status" = 'approved'
   )`, userID)
	if err != nil { return nil, fmt.Errorf("query medicines: %w", err) }
	defer rows.Close()
	var result []medicine
	for rows.Next() {
		var m medicine
		if err := rows.Scan(&m.ID, &m.PatientID, &m.Name, &m.PatientName, &m.Frequency, &m.SpecificDays, &m.Times, &m.CycleEvery, &m.CycleUnit, &m.DurationType, &m.DurationValue, &m.DurationUnit, &m.StartDate, &m.Supply, &m.RefillThreshold, &m.IsRecurring, &m.PauseStart, &m.PauseEnd); err != nil { return nil, err }
		result = append(result, m)
	}
	return result, rows.Err()
}

func (s *Scheduler) processMedicine(ctx context.Context, u user, m medicine, today, now time.Time, loc *time.Location) error {
	if m.IsRecurring && m.Supply <= m.RefillThreshold {
		if err := s.notifyOnce(ctx, u.ID, "", "refill_low", now.Truncate(24*time.Hour), "Refill reminder", fmt.Sprintf("%s for %s has %d doses remaining.", m.Name, m.PatientName, m.Supply), map[string]string{"medicineId": m.ID, "patientId": m.PatientID}); err != nil {
			s.log.Warn("refill notification failed", "medicine_id", m.ID, "error", err)
		}
	}
	if !occurs(m, today) { return nil }
	var times []string
	if err := json.Unmarshal([]byte(m.Times), &times); err != nil { return fmt.Errorf("parse medicine times: %w", err) }
	for _, raw := range times {
		parsed, err := time.Parse("15:04", raw)
		if err != nil { continue }
		scheduled := time.Date(today.Year(), today.Month(), today.Day(), parsed.Hour(), parsed.Minute(), 0, 0, loc)
		doseID, err := s.ensureDose(ctx, m, today, raw)
		if err != nil { return err }
		if err := s.processDose(ctx, u, m, doseID, raw, scheduled, now); err != nil {
			s.log.Warn("dose notification failed", "dose_id", doseID, "error", err)
		}
	}
	return nil
}

func (s *Scheduler) ensureDose(ctx context.Context, m medicine, day time.Time, doseTime string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `
INSERT INTO "DoseEvents" ("Id","PatientId","MedicineId","Date","Time","Status")
VALUES (gen_random_uuid(),$1,$2,$3::date,$4,'pending')
ON CONFLICT ("PatientId","MedicineId","Date","Time") DO UPDATE SET "Time"=EXCLUDED."Time"
RETURNING "Id"`, m.PatientID, m.ID, dateOnly(day), doseTime).Scan(&id)
	if err != nil { return "", fmt.Errorf("upsert dose event: %w", err) }
	return id, nil
}

func (s *Scheduler) processDose(ctx context.Context, u user, m medicine, doseID, doseTime string, scheduled, now time.Time) error {
	var status string
	if err := s.db.QueryRowContext(ctx, `SELECT "Status" FROM "DoseEvents" WHERE "Id"=$1`, doseID).Scan(&status); err != nil { return err }
	if status == "taken" || status == "skipped" || status == "rescheduled" { return nil }
	minutesUntil := scheduled.Sub(now).Minutes()
	if minutesUntil > 0 && minutesUntil <= float64(max(0, u.LeadMinutes)) {
		occurrence := now.Truncate(time.Minute)
		if err := s.deliverOnce(ctx, u.ID, doseID, "dose_reminder", occurrence, "Medicine reminder", fmt.Sprintf("%s for %s is due at %s.", m.Name, m.PatientName, doseTime), "dose_reminder"); err != nil { return err }
	}
	if u.FinalEnabled && minutesUntil <= 0 && minutesUntil > -1 {
		if err := s.deliverOnce(ctx, u.ID, doseID, "dose_final", scheduled, "Medicine due now", fmt.Sprintf("%s for %s is due now.", m.Name, m.PatientName), "dose_final"); err != nil { return err }
	}
	if minutesUntil <= 0 && status == "pending" {
		_, err := s.db.ExecContext(ctx, `UPDATE "DoseEvents" SET "Status"='missed',"MissedAt"=now() WHERE "Id"=$1 AND "Status"='pending'`, doseID)
		if err != nil { return err }
		status = "missed"
	}
	if minutesUntil <= 0 && status == "missed" {
		if err := s.deliverOnce(ctx, u.ID, doseID, "dose_missed", scheduled, "Dose missed", fmt.Sprintf("%s for %s was not marked taken at %s.", m.Name, m.PatientName, doseTime), "dose_missed"); err != nil { return err }
	}
	return nil
}

func (s *Scheduler) deliverOnce(ctx context.Context, userID, doseID, kind string, scheduled time.Time, title, body, notificationType string) error {
	var deliveryID string
	err := s.db.QueryRowContext(ctx, `
INSERT INTO "NotificationDeliveries" ("Id","UserId","DoseEventId","Type","ScheduledFor","Status")
VALUES (gen_random_uuid(),$1,$2,$3,$4,'pending')
ON CONFLICT ("DoseEventId","UserId","Type","ScheduledFor") DO NOTHING
RETURNING "Id"`, userID, doseID, kind, scheduled).Scan(&deliveryID)
	if errors.Is(err, sql.ErrNoRows) { return nil }
	if err != nil { return fmt.Errorf("reserve delivery: %w", err) }
	err = s.sendToUser(ctx, userID, title, body, notificationType, doseID)
	status := "sent"
	var errorText any
	var sentAt any = time.Now().UTC()
	if err != nil { status = "failed"; errorText = err.Error(); sentAt = nil }
	_, updateErr := s.db.ExecContext(ctx, `UPDATE "NotificationDeliveries" SET "Status"=$2,"SentAt"=$3,"Error"=$4 WHERE "Id"=$1`, deliveryID, status, sentAt, errorText)
	if updateErr != nil { return updateErr }
	if err != nil {
		return s.addNotification(ctx, userID, "notification_failed", "Notification not delivered", title+": "+body, map[string]string{"doseId": doseID, "type": notificationType})
	}
	return nil
}

func (s *Scheduler) notifyOnce(ctx context.Context, userID, doseID, kind string, scheduled time.Time, title, body string, data map[string]string) error {
	// A daily unique delivery reservation prevents duplicate low-supply alerts.
	var id string
	err := s.db.QueryRowContext(ctx, `
INSERT INTO "NotificationDeliveries" ("Id","UserId","DoseEventId","Type","ScheduledFor","Status")
VALUES (gen_random_uuid(),$1,NULL,$2,$3,'pending')
ON CONFLICT ("DoseEventId","UserId","Type","ScheduledFor") DO NOTHING
RETURNING "Id"`, userID, kind, scheduled).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) { return nil }
	if err != nil { return err }
	err = s.sendToUser(ctx, userID, title, body, kind, doseID)
	status := "sent"
	var sentAt any = time.Now().UTC()
	var errorText any
	if err != nil { status = "failed"; sentAt = nil; errorText = err.Error() }
	_, updateErr := s.db.ExecContext(ctx, `UPDATE "NotificationDeliveries" SET "Status"=$2,"SentAt"=$3,"Error"=$4 WHERE "Id"=$1`, id, status, sentAt, errorText)
	if updateErr != nil { return updateErr }
	return s.addNotification(ctx, userID, kind, title, body, data)
}

func (s *Scheduler) sendToUser(ctx context.Context, userID, title, body, kind, doseID string) error {
	rows, err := s.db.QueryContext(ctx, `SELECT "Id","Endpoint","P256dh","Auth" FROM "PushSubscriptions" WHERE "UserId"=$1`, userID)
	if err != nil { return err }
	defer rows.Close()
	sent := false
	var subscriptions []notifier.Subscription
	for rows.Next() {
		var sub notifier.Subscription
		if err := rows.Scan(&sub.ID, &sub.Endpoint, &sub.P256dh, &sub.Auth); err != nil { return err }
		subscriptions = append(subscriptions, sub)
	}
	if err := rows.Err(); err != nil { return err }
	for _, sub := range subscriptions {
		if err := s.push.Send(ctx, sub, notifier.Message{Title:title, Body:body, Type:kind, DoseID:doseID, URL:"/"}); err != nil {
			s.log.Warn("push subscription delivery failed", "subscription_id", sub.ID, "error", err)
			continue
		}
		sent = true
		_, _ = s.db.ExecContext(ctx, `UPDATE "PushSubscriptions" SET "UpdatedAt"=now() WHERE "Id"=$1`, sub.ID)
	}
	if !sent { return fmt.Errorf("no successful browser push delivery") }
	return nil
}

func (s *Scheduler) addNotification(ctx context.Context, userID, kind, title, body string, data map[string]string) error {
	encoded, _ := json.Marshal(data)
	_, err := s.db.ExecContext(ctx, `
INSERT INTO "Notifications" ("Id","UserId","Type","Title","Message","DataJson","IsRead","CreatedAt")
VALUES (gen_random_uuid(),$1,$2,$3,$4,$5,false,now())`, userID, kind, title, body, string(encoded))
	return err
}

func occurs(m medicine, day time.Time) bool {
	start := dateOnly(m.StartDate)
	current := dateOnly(day)
	if current.Before(start) { return false }
	if m.PauseStart.Valid {
		pauseStart := dateOnly(m.PauseStart.Time)
		if !current.Before(pauseStart) && (!m.PauseEnd.Valid || !current.After(dateOnly(m.PauseEnd.Time))) { return false }
	}
	diff := int(current.Sub(start).Hours() / 24)
	if !m.IsRecurring && diff != 0 { return false }
	if m.DurationType != "ongoing" {
		duration := m.DurationValue
		switch strings.ToLower(m.DurationUnit) { case "weeks": duration *= 7; case "months": duration *= 30 }
		if diff >= duration { return false }
	}
	switch m.Frequency {
	case "daily": return true
	case "everyOtherDay": return diff%2 == 0
	case "specificDays":
		var days []string
		_ = json.Unmarshal([]byte(m.SpecificDays), &days)
		for _, d := range days { if strings.EqualFold(d, current.Weekday().String()[:3]) { return true } }
		return false
	case "recurringCycle":
		cycle := max(1, m.CycleEvery)
		switch m.CycleUnit {
		case "weeks": return diff%(cycle*7) == 0
		case "months": return current.Day() == start.Day()
		default: return diff%cycle == 0
		}
	default: return false
	}
}

func dateOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

