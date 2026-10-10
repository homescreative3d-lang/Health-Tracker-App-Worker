package scheduler

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

//go:embed schema.sql
var schemaSQL string

const scheduleHorizonDays = 7
const jobBatchSize = 50

type notificationJob struct {
	ID, DedupKey, UserID, Type, Title, Message, DataJSON string
	DoseEventID, MedicineID                              sql.NullString
	ScheduledFor, NextAttemptAt                           time.Time
	Attempts                                              int
}

// RunEventDriven uses PostgreSQL notifications to react to schedule changes and
// durable jobs to deliver reminders. A low-frequency recovery timeout protects
// against missed LISTEN/NOTIFY messages and worker restarts.
func (s *Scheduler) RunEventDriven(ctx context.Context, databaseURL string, recoveryInterval time.Duration) error {
	if recoveryInterval < 30*time.Second {
		return fmt.Errorf("recovery interval must be at least 30s")
	}
	listener, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("open PostgreSQL notification listener: %w", err)
	}
	defer listener.Close(context.Background())
	if _, err := listener.PgConn().Exec(ctx, schemaSQL).ReadAll(); err != nil {
		return fmt.Errorf("apply notification worker schema: %w", err)
	}
	if _, err := listener.Exec(ctx, "LISTEN notification_schedule_changed"); err != nil {
		return fmt.Errorf("listen for schedule changes: %w", err)
	}
	notifications := make(chan error, 1)
	go func() {
		for {
			_, waitErr := listener.WaitForNotification(ctx)
			select {
			case notifications <- waitErr:
			case <-ctx.Done():
				return
			}
			if waitErr != nil {
				return
			}
		}
	}()
	if err := s.reconcileJobs(ctx); err != nil {
		return fmt.Errorf("initial schedule reconciliation: %w", err)
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.processOutbox(ctx); err != nil {
			s.log.Error("outbox processing failed", "error", err)
		}
		if err := s.processDueJobs(ctx); err != nil {
			s.log.Error("due job processing failed", "error", err)
		}

		wait := recoveryInterval
		next, err := s.nextWake(ctx)
		if err != nil {
			s.log.Warn("could not calculate next worker wake", "error", err)
		} else if next <= 0 {
			wait = time.Second
		} else if next < wait {
			wait = next
		}
		if wait < time.Second {
			wait = time.Second
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select { case <-timer.C: default: }
			}
			return ctx.Err()
		case waitErr := <-notifications:
			if !timer.Stop() {
				select { case <-timer.C: default: }
			}
			if waitErr != nil {
				return fmt.Errorf("PostgreSQL notification listener stopped: %w", waitErr)
			}
		case <-timer.C:
		}
	}
}

func (s *Scheduler) processOutbox(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT "Id" FROM "NotificationOutbox" WHERE "ProcessedAt" IS NULL ORDER BY "CreatedAt" LIMIT 500`)
	if err != nil { return err }
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil { _ = rows.Close(); return err }
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil { _ = rows.Close(); return err }
	_ = rows.Close()
	if len(ids) == 0 { return nil }
	if err := s.reconcileJobs(ctx); err != nil { return err }
	for _, id := range ids {
		if _, err := s.db.ExecContext(ctx, `UPDATE "NotificationOutbox" SET "ProcessedAt"=now() WHERE "Id"=$1 AND "ProcessedAt" IS NULL`, id); err != nil { return err }
	}
	return nil
}
func (s *Scheduler) reconcileJobs(ctx context.Context) error {
	now := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx, `UPDATE "NotificationJobs" SET "Status"='cancelled',"UpdatedAt"=now() WHERE "Status"='pending'`); err != nil {
		return err
	}
	users, err := s.loadUsers(ctx)
	if err != nil {
		return err
	}
	for _, u := range users {
		loc, err := time.LoadLocation(u.TimeZone)
		if err != nil {
			loc, _ = time.LoadLocation(s.fallback)
		}
		localNow := now.In(loc)
		medicines, err := s.loadMedicines(ctx, u.ID)
		if err != nil {
			return fmt.Errorf("load medicines for user %s: %w", u.ID, err)
		}
		for _, m := range medicines {
			for offset := 0; offset < scheduleHorizonDays; offset++ {
				day := dateOnly(localNow.AddDate(0, 0, offset))
				if m.IsRecurring && m.Supply <= m.RefillThreshold {
					refillAt := time.Date(day.Year(), day.Month(), day.Day(), 9, 0, 0, 0, loc)
					if !refillAt.Before(now) {
						data, _ := json.Marshal(map[string]string{"medicineId": m.ID, "patientId": m.PatientID})
						key := fmt.Sprintf("refill:%s:%s:%s", u.ID, m.ID, day.Format("2006-01-02"))
						if err := s.upsertJob(ctx, key, u.ID, "", m.ID, "refill_low", refillAt, "Refill reminder", fmt.Sprintf("%s for %s has %d doses remaining.", m.Name, m.PatientName, m.Supply), string(data)); err != nil {
							return err
						}
					}
				}
				if !occurs(m, day) {
					continue
				}
				var times []string
				if err := json.Unmarshal([]byte(m.Times), &times); err != nil {
					return fmt.Errorf("parse times for medicine %s: %w", m.ID, err)
				}
				for _, raw := range times {
					parsed, err := time.Parse("15:04", raw)
					if err != nil {
						continue
					}
					scheduled := time.Date(day.Year(), day.Month(), day.Day(), parsed.Hour(), parsed.Minute(), 0, 0, loc)
					doseID, err := s.ensureDose(ctx, m, day, raw)
					if err != nil {
						return err
					}
					data, _ := json.Marshal(map[string]string{"doseId": doseID, "medicineId": m.ID, "patientId": m.PatientID})
					if u.LeadMinutes > 0 {
						repeat := max(1, u.RepeatMinutes)
						for reminderAt, slot := scheduled.Add(-time.Duration(u.LeadMinutes)*time.Minute), 0; reminderAt.Before(scheduled) && slot < 100; reminderAt, slot = reminderAt.Add(time.Duration(repeat)*time.Minute), slot+1 {
							if reminderAt.Before(now) {
								continue
							}
							key := fmt.Sprintf("reminder:%s:%s:%s", u.ID, doseID, reminderAt.UTC().Format(time.RFC3339))
							if err := s.upsertJob(ctx, key, u.ID, doseID, m.ID, "dose_reminder", reminderAt, "Medicine reminder", fmt.Sprintf("%s for %s is due at %s.", m.Name, m.PatientName, raw), string(data)); err != nil {
								return err
							}
						}
					}
					if u.FinalEnabled && !scheduled.Before(now) {
						key := fmt.Sprintf("final:%s:%s", u.ID, doseID)
						if err := s.upsertJob(ctx, key, u.ID, doseID, m.ID, "dose_final", scheduled, "Medicine due now", fmt.Sprintf("%s for %s is due now.", m.Name, m.PatientName), string(data)); err != nil {
							return err
						}
					}
					missedAt := scheduled.Add(time.Minute)
					key := fmt.Sprintf("missed:%s:%s", u.ID, doseID)
					if err := s.upsertJob(ctx, key, u.ID, doseID, m.ID, "dose_missed", missedAt, "Dose missed", fmt.Sprintf("%s for %s was not marked taken at %s.", m.Name, m.PatientName, raw), string(data)); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func (s *Scheduler) upsertJob(ctx context.Context, key, userID, doseID, medicineID, kind string, scheduled time.Time, title, message, data string) error {
	var doseValue, medicineValue any
	if doseID != "" {
		doseValue = doseID
	}
	if medicineID != "" {
		medicineValue = medicineID
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO "NotificationJobs"
    ("DedupKey","UserId","DoseEventId","MedicineId","Type","ScheduledFor","NextAttemptAt","Title","Message","DataJson")
VALUES ($1,$2,$3,$4,$5,$6,$6,$7,$8,$9)
ON CONFLICT ("DedupKey") DO UPDATE
SET "Status"='pending',"NextAttemptAt"=EXCLUDED."NextAttemptAt","Title"=EXCLUDED."Title",
    "Message"=EXCLUDED."Message","DataJson"=EXCLUDED."DataJson',"UpdatedAt"=now(),"LockedUntil"=NULL
WHERE "NotificationJobs"."Status"='cancelled'`, key, userID, doseValue, medicineValue, kind, scheduled.UTC(), title, message, data)
	if err != nil {
		return fmt.Errorf("upsert notification job %s: %w", key, err)
	}
	return nil
}

func (s *Scheduler) processDueJobs(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `
WITH due AS (
    SELECT "Id" FROM "NotificationJobs"
    WHERE ("Status"='pending' AND "NextAttemptAt" <= now())
       OR ("Status"='processing' AND "LockedUntil" < now())
    ORDER BY "NextAttemptAt"
    LIMIT $1
    FOR UPDATE SKIP LOCKED
)
UPDATE "NotificationJobs" j
SET "Status"='processing',"LockedUntil"=now()+interval '2 minutes',"Attempts"=j."Attempts"+1,"UpdatedAt"=now()
FROM due WHERE j."Id"=due."Id"
RETURNING j."Id",j."DedupKey",j."UserId",j."DoseEventId",j."MedicineId",j."Type",j."ScheduledFor",j."NextAttemptAt",j."Title",j."Message",j."DataJson",j."Attempts"`, jobBatchSize)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	var jobs []notificationJob
	for rows.Next() {
		var job notificationJob
		if err := rows.Scan(&job.ID, &job.DedupKey, &job.UserID, &job.DoseEventID, &job.MedicineID, &job.Type, &job.ScheduledFor, &job.NextAttemptAt, &job.Title, &job.Message, &job.DataJSON, &job.Attempts); err != nil {
			_ = rows.Close()
			_ = tx.Rollback()
			return err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		_ = tx.Rollback()
		return err
	}
	_ = rows.Close()
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, job := range jobs {
		if err := s.executeJob(ctx, job); err != nil {
			s.log.Error("notification job failed", "job_id", job.ID, "type", job.Type, "error", err)
			delay := time.Duration(min(job.Attempts, 20)*15) * time.Second
			if delay > 5*time.Minute {
				delay = 5 * time.Minute
			}
			_, updateErr := s.db.ExecContext(ctx, `UPDATE "NotificationJobs" SET "Status"='pending',"NextAttemptAt"=now()+$2,"LockedUntil"=NULL,"LastError"=$3,"UpdatedAt"=now() WHERE "Id"=$1`, job.ID, delay, err.Error())
			if updateErr != nil {
				return updateErr
			}
			continue
		}
		_, err := s.db.ExecContext(ctx, `UPDATE "NotificationJobs" SET "Status"='sent',"LockedUntil"=NULL,"LastError"=NULL,"UpdatedAt"=now() WHERE "Id"=$1`, job.ID)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Scheduler) executeJob(ctx context.Context, job notificationJob) error {
	if job.DoseEventID.Valid {
		var status string
		if err := s.db.QueryRowContext(ctx, `SELECT "Status" FROM "DoseEvents" WHERE "Id"=$1`, job.DoseEventID.String).Scan(&status); err != nil {
			return err
		}
		if status == "taken" || status == "skipped" || status == "rescheduled" {
			return nil
		}
		if job.Type == "dose_missed" && status == "pending" {
			if _, err := s.db.ExecContext(ctx, `UPDATE "DoseEvents" SET "Status"='missed',"MissedAt"=now() WHERE "Id"=$1 AND "Status"='pending'`, job.DoseEventID.String); err != nil {
				return err
			}
		}
	}
	if err := s.insertNotificationOnce(ctx, job); err != nil {
		return err
	}
	if err := s.recordAndSendPush(ctx, job); err != nil {
		s.log.Warn("push delivery failed; in-app notification is retained", "job_id", job.ID, "error", err)
	}
	return nil
}

func (s *Scheduler) insertNotificationOnce(ctx context.Context, job notificationJob) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO "Notifications" ("Id","UserId","Type","Title","Message","DataJson","IsRead","CreatedAt","IdempotencyKey")
VALUES (gen_random_uuid(),$1,$2,$3,$4,$5,false,now(),$6)
ON CONFLICT ("IdempotencyKey") WHERE "IdempotencyKey" IS NOT NULL DO NOTHING`,
		job.UserID, job.Type, job.Title, job.Message, job.DataJSON, job.DedupKey)
	return err
}

func (s *Scheduler) recordAndSendPush(ctx context.Context, job notificationJob) error {
	var deliveryID string
	err := s.db.QueryRowContext(ctx, `
INSERT INTO "NotificationDeliveries" ("Id","UserId","DoseEventId","Type","ScheduledFor","Status")
VALUES (gen_random_uuid(),$1,$2,$3,$4,'pending')
ON CONFLICT ("DoseEventId","UserId","Type","ScheduledFor") DO UPDATE
SET "Status"=CASE WHEN "NotificationDeliveries"."Status"='sent' THEN 'sent' ELSE 'pending' END
RETURNING "Id"`, job.UserID, nullableString(job.DoseEventID), job.Type, job.ScheduledFor).Scan(&deliveryID)
	if err != nil {
		return err
	}
	var currentStatus string
	if err := s.db.QueryRowContext(ctx, `SELECT "Status" FROM "NotificationDeliveries" WHERE "Id"=$1`, deliveryID).Scan(&currentStatus); err != nil {
		return err
	}
	if currentStatus == "sent" {
		return nil
	}
	err = s.sendToUser(ctx, job.UserID, job.Title, job.Message, job.Type, job.DoseEventID.String)
	status := "sent"
	var errorText any
	var sentAt any = time.Now().UTC()
	if err != nil {
		status = "failed"
		errorText = err.Error()
		sentAt = nil
	}
	if _, updateErr := s.db.ExecContext(ctx, `UPDATE "NotificationDeliveries" SET "Status"=$2,"SentAt"=$3,"Error"=$4 WHERE "Id"=$1`, deliveryID, status, sentAt, errorText); updateErr != nil {
		return updateErr
	}
	return err
}

func nullableString(value sql.NullString) any {
	if value.Valid {
		return value.String
	}
	return nil
}

func (s *Scheduler) nextWake(ctx context.Context) (time.Duration, error) {
	var next sql.NullTime
	err := s.db.QueryRowContext(ctx, `
SELECT MIN(CASE WHEN "Status"='processing' THEN "LockedUntil" ELSE "NextAttemptAt" END)
FROM "NotificationJobs" WHERE "Status" IN ('pending','processing')`).Scan(&next)
	if err != nil {
		return 0, err
	}
	if !next.Valid {
		return 0, nil
	}
	return time.Until(next.Time), nil
}
