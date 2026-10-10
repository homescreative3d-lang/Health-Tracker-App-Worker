ALTER TABLE "Notifications" ADD COLUMN IF NOT EXISTS "IdempotencyKey" text;
CREATE UNIQUE INDEX IF NOT EXISTS "IX_Notifications_IdempotencyKey"
    ON "Notifications" ("IdempotencyKey")
    WHERE "IdempotencyKey" IS NOT NULL;

CREATE TABLE IF NOT EXISTS "NotificationOutbox" (
    "Id" uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    "EntityType" text NOT NULL,
    "EntityId" uuid NOT NULL,
    "Operation" text NOT NULL,
    "CreatedAt" timestamptz NOT NULL DEFAULT now(),
    "ProcessedAt" timestamptz
);
CREATE INDEX IF NOT EXISTS "IX_NotificationOutbox_Unprocessed"
    ON "NotificationOutbox" ("CreatedAt") WHERE "ProcessedAt" IS NULL;

CREATE TABLE IF NOT EXISTS "NotificationJobs" (
    "Id" uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    "DedupKey" text NOT NULL UNIQUE,
    "UserId" uuid NOT NULL,
    "DoseEventId" uuid,
    "MedicineId" uuid,
    "Type" text NOT NULL,
    "ScheduledFor" timestamptz NOT NULL,
    "NextAttemptAt" timestamptz NOT NULL,
    "Title" text NOT NULL,
    "Message" text NOT NULL,
    "DataJson" text NOT NULL DEFAULT '{}',
    "Status" text NOT NULL DEFAULT 'pending',
    "Attempts" integer NOT NULL DEFAULT 0,
    "LockedUntil" timestamptz,
    "LastError" text,
    "CreatedAt" timestamptz NOT NULL DEFAULT now(),
    "UpdatedAt" timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS "IX_NotificationJobs_Due"
    ON "NotificationJobs" ("Status", "NextAttemptAt");
CREATE INDEX IF NOT EXISTS "IX_NotificationJobs_ExpiredLocks"
    ON "NotificationJobs" ("Status", "LockedUntil");

CREATE OR REPLACE FUNCTION "enqueue_notification_schedule_change"()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    entity_id uuid;
BEGIN
    IF TG_OP = 'DELETE' THEN
        entity_id := OLD."Id";
    ELSE
        entity_id := NEW."Id";
    END IF;
    INSERT INTO "NotificationOutbox" ("EntityType", "EntityId", "Operation")
    VALUES (TG_TABLE_NAME, entity_id, TG_OP);
    PERFORM pg_notify('notification_schedule_changed', TG_TABLE_NAME);
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS "TR_Users_NotificationSchedule" ON "Users";
CREATE TRIGGER "TR_Users_NotificationSchedule"
AFTER INSERT OR UPDATE OR DELETE ON "Users"
FOR EACH ROW EXECUTE FUNCTION "enqueue_notification_schedule_change"();

DROP TRIGGER IF EXISTS "TR_Medicines_NotificationSchedule" ON "Medicines";
CREATE TRIGGER "TR_Medicines_NotificationSchedule"
AFTER INSERT OR UPDATE OR DELETE ON "Medicines"
FOR EACH ROW EXECUTE FUNCTION "enqueue_notification_schedule_change"();

DROP TRIGGER IF EXISTS "TR_FamilyMembers_NotificationSchedule" ON "FamilyMembers";
CREATE TRIGGER "TR_FamilyMembers_NotificationSchedule"
AFTER INSERT OR UPDATE OR DELETE ON "FamilyMembers"
FOR EACH ROW EXECUTE FUNCTION "enqueue_notification_schedule_change"();
