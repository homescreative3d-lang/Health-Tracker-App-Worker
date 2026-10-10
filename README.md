# TENDED Notification Worker

A Go worker for TENDED medicine scheduling and notification delivery. It is designed to take over notification scheduling from the .NET API in a controlled migration.

## Current scope

- Polls PostgreSQL for users, patients, medicines, and today's scheduled doses.
- Creates missing dose events idempotently.
- Emits lead-time, due-now, missed-dose, and low-supply notifications.
- Sends browser push notifications using Web Push / VAPID.
- Records notification delivery outcomes and removes expired subscriptions.
- Uses a graceful shutdown, structured logs, bounded polling, and explicit configuration.

> Migration safety: this worker writes to the existing TENDED tables. Do not run it alongside the .NET scheduler until the .NET notification scheduler is disabled; otherwise both processes may compete to send notifications. Database uniqueness constraints remain the final idempotency guard.

## Configuration

Required:
- `DATABASE_URL` — PostgreSQL URL or standard libpq-style connection URL.
- `WEBPUSH_PUBLIC_KEY`, `WEBPUSH_PRIVATE_KEY` — same VAPID pair configured for the frontend.
- `WEBPUSH_SUBJECT` — e.g. `mailto:notifications@tended.app`.

Optional:
- `POLL_INTERVAL` — default `15s`.
- `LOG_LEVEL` — `debug`, `info`, `warn`, or `error`.
- `APP_TIMEZONE_FALLBACK` — default `Asia/Kolkata`.

Never commit credentials. Configure them as environment variables in the hosting provider.

## Local development

```sh
cp .env.example .env
# Set real values in your shell or hosting environment; do not commit .env.
go mod download
go test ./...
go run ./cmd/worker
```

## Deployment

Deploy as a continuously running Go worker (not a request-driven web service). Use a host that supports long-running background processes and configure the environment variables above. A free instance that sleeps is not reliable for time-sensitive medicine reminders.

## Health and observability

The process logs startup configuration (never secrets), tick duration, and per-operation errors. It exits non-zero when required configuration is missing. A lightweight HTTP health endpoint is intentionally not included because this worker does not serve application traffic; use process health checks and logs.

## Migration plan

1. Deploy with notification scheduling disabled in the .NET service, or run this worker only in a non-production test database first.
2. Verify doses, reminder lead time, final reminder, missed-dose timing, low-supply alerts, and delivery history.
3. Ensure the existing database schema has the expected unique indexes.
4. Enable this worker in production, then remove notification scheduling/push-delivery logic from .NET in a separate reviewed change.
