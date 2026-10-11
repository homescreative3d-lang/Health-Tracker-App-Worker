# TENDED Notification Worker

A Go worker for durable, platform-independent medicine notifications. PostgreSQL is the source of truth; the same scheduler can serve the current React web app and a future Android app.

## Runtime provider switch (shared with .NET)

The worker and .NET API use the same singleton PostgreSQL table, `NotificationRuntimeConfig`:

- `Enabled = false`: neither scheduler is authorized to process notifications.
- `Enabled = true, ActiveProvider = 'dotnet'`: only the existing .NET scheduler may process notifications.
- `Enabled = true, ActiveProvider = 'go'`: only this Go worker may process notifications.

The default row is deliberately `Enabled=true, ActiveProvider='dotnet'`, so installing the worker does not silently activate it. The worker reads this flag before reconciling or delivering jobs, and the database sends a PostgreSQL notification when the setting changes. It also checks the setting again during recovery. Missing/unreadable config fails closed (no notifications).

Example switch (run only after the worker is tested and deployed):
```sql
UPDATE "NotificationRuntimeConfig"
SET "Enabled" = true, "ActiveProvider" = 'go', "UpdatedAt" = now()
WHERE "Id" = 1;
```

Switch back to .NET:
```sql
UPDATE "NotificationRuntimeConfig"
SET "Enabled" = true, "ActiveProvider" = 'dotnet', "UpdatedAt" = now()
WHERE "Id" = 1;
```

Pause both:
```sql
UPDATE "NotificationRuntimeConfig"
SET "Enabled" = false, "UpdatedAt" = now()
WHERE "Id" = 1;
```

**Never enable both schedulers at the same time.** The database flag only selects the active provider; it does not deploy/start the Go worker.

## Architecture

- Medicine, user-preference, and family-membership changes write durable rows to `NotificationOutbox` through PostgreSQL triggers.
- PostgreSQL `LISTEN/NOTIFY` wakes the worker immediately after a committed change.
- The worker rebuilds the next seven days of reminder jobs in `NotificationJobs`; jobs survive restarts and are claimed with `FOR UPDATE SKIP LOCKED`.
- A low-frequency recovery timeout checks the durable outbox in case the listener was disconnected or a notification was missed. It does not repeatedly scan every medicine on a short polling interval.
- Every scheduled notification is stored in the app's `Notifications` table with an idempotency key, so the in-app bell works even when browser push is unavailable.
- Web Push is the current delivery adapter. An Android/FCM adapter can be added without changing scheduling or job persistence.

## Configuration

Required:
- `DATABASE_URL`

Optional (browser push is disabled if either key is missing):
- `WEBPUSH_PUBLIC_KEY`
- `WEBPUSH_PRIVATE_KEY`

Optional:
- `WEBPUSH_SUBJECT` (defaults to `mailto:notifications@tended.app`)
- `RECOVERY_INTERVAL` (Go duration, defaults to `20s`, minimum `20s`)
- `LOG_LEVEL` (`debug`, `info`, `warn`, `error`)
- `APP_TIMEZONE_FALLBACK` (defaults to `Asia/Kolkata`)

The worker applies its idempotent schema and trigger setup on startup. The database role therefore needs permission to create tables, indexes, and triggers. Test against a separate database before using production data.

## Local development (Windows / PowerShell)

Start the API repository's local PostgreSQL service first:

```powershell
docker compose -f docker-compose.dev.yml up -d postgres
```

Start the API once so it initializes the application tables and `NotificationRuntimeConfig`, then switch the provider to `go` in DBeaver or psql:

```sql
UPDATE "NotificationRuntimeConfig"
SET "Enabled" = true, "ActiveProvider" = 'go', "UpdatedAt" = now()
WHERE "Id" = 1;
```

From this repository, set the local worker environment and run the checks:

```powershell
$env:DATABASE_URL = "postgres://tended:tended_local_only@localhost:5433/tended_dev?sslmode=disable"
$env:APP_TIMEZONE_FALLBACK = "Asia/Kolkata"
$env:RECOVERY_INTERVAL = "20s"

go mod tidy
go test ./...
go vet ./...
go run ./cmd/worker
```

The worker can run without VAPID keys for local schedule/in-app notification testing; browser push is disabled until both `WEBPUSH_PUBLIC_KEY` and `WEBPUSH_PRIVATE_KEY` are configured. When you stop the worker, switch `ActiveProvider` back to `dotnet` to re-enable the .NET scheduler. Never run both as active providers.

## Operational safety

- Do not run this worker and the .NET `NotificationScheduler` as active providers simultaneously.
- This branch does not deploy the worker or disable the .NET scheduler.
- A sleeping free service cannot guarantee on-time delivery; use an always-on worker for reliable medication reminders.
