# TENDED Notification Worker

Go-based medicine scheduling and notification worker. See deployment, configuration, migration-safety notes, and local commands below.

## Configure

Set `DATABASE_URL`, `WEBPUSH_PUBLIC_KEY`, `WEBPUSH_PRIVATE_KEY`, and optionally `WEBPUSH_SUBJECT`, `POLL_INTERVAL`, `LOG_LEVEL`, and `APP_TIMEZONE_FALLBACK`. Never commit secrets.

## Run locally

```sh
go mod tidy
go test ./...
go vet ./...
go run ./cmd/worker
```

## Important migration safety

This worker reads/writes the existing TENDED PostgreSQL tables and sends browser push. Do not run it at the same time as the .NET notification scheduler in production; duplicate notifications are possible until both implementations coordinate through the same delivery reservation strategy. Test against a separate database first, verify delivery history, then disable the .NET scheduler in a separate reviewed change.

Deploy as a continuously running Go worker. A sleeping service is not reliable for time-sensitive medicine reminders.
