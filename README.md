# TENDED Notification Worker

A Go worker for durable, platform-independent medicine notifications. PostgreSQL is the source of truth; the same scheduler can serve the current React web app and a future Android app.

## Architecture

- Medicine, user notification-preference, and family-membership changes write durable rows to `NotificationOutbox` through PostgreSQL triggers.
- PostgreSQL `LISTEN/NOTIFY` wakes the worker immediately after a committed change.
- The worker rebuilds the next seven days of reminder jobs in `NotificationJobs`; jobs survive restarts and are claimed with `FOR UPDATE SKIP LOCKED`.
- A low-frequency recovery timeout checks the durable outbox in case the listener was disconnected or a notification was missed. It does not repeatedly scan every medicine on a short polling interval.
- Every scheduled notification is stored in the app's `Notifications` table with an idempotency key, so the in-app bell works even when browser push is unavailable.
- Web Push is the current delivery adapter. An Android/FCM adapter can be added without changing scheduling or job persistence.

## Configuration

Required:
- `DATABASE_URL`
- `WEBPUSH_PUBLIC_KEY`
- `WEBPUSH_PRIVATE_KEY`

Optional:
- `WEBPUSH_SUBJECT` (defaults to `mailto:notifications@tended.app`)
- `RECOVERY_INTERVAL` (Go duration, defaults to `5m`, minimum `30s`)
- `LOG_LEVEL` (`debug`, `info`, `warn`, `error`)
- `APP_TIMEZONE_FALLBACK` (defaults to `Asia/Kolkata`)

The worker applies its idempotent schema and trigger setup on startup. The database role therefore needs permission to create tables, indexes, and triggers. Test against a separate database before using production data.

## Local development

```sh
go mod tidy
go test ./...
go vet ./...
go run ./cmd/worker
```

## Operational safety

**Do not run this worker and the .NET `NotificationScheduler` at the same time in production.** The .NET scheduler must be disabled only after the Go worker is tested against a separate database and its push/in-app delivery behavior is verified. This PR does not disable the .NET scheduler or deploy the worker.

For reliable time-sensitive reminders, run the worker on an always-on service. A sleeping free service cannot guarantee on-time delivery.
