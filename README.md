# Seat Reservation Service

A Go and PostgreSQL API for creating shows, placing temporary seat holds, confirming or cancelling reservations, and exposing service metrics. Prices are integer paise end to end.

## Run locally

Requirements: Docker with Compose. The first start builds the Go service, initializes PostgreSQL from `db/schema.sql`, and starts Prometheus.

```sh
cp .env.example .env
docker compose up --build -d
```

Check readiness and logs:

```sh
curl http://localhost:8080/readyz
docker compose logs -f app
```

The database and Prometheus UI bind to localhost. Prometheus is at <http://localhost:9090>; the service's scrape endpoint is <http://localhost:8080/metrics>. The persistent `postgres_data` volume keeps database state across container restarts. `docker compose down` stops the stack and preserves that volume.

## API quick start

Create a show and note the returned `id`:

```sh
curl -X POST http://localhost:8080/shows \
  -H 'Content-Type: application/json' \
  -d '{"name":"Friday show","price_paise":25000,"seats":["A1","A2","A3"]}'
```

Reserve a seat. A successful reserve creates a 15-minute hold; use its `reservation_id` to confirm or cancel it.

```sh
curl -X POST http://localhost:8080/shows/SHOW_ID/reserve \
  -H 'Content-Type: application/json' \
  -H 'X-User-ID: user-123' \
  -H 'Idempotency-Key: request-123' \
  -d '{"seats":["A1"]}'

curl -X POST http://localhost:8080/reservations/RESERVATION_ID/confirm \
  -H 'X-User-ID: user-123'
```

`X-User-ID` is an application-supplied identity for this assignment, not authentication. Put the API behind an authenticated gateway before using it for real customers.

## One-command burst

With the local stack running, use the included script from the repository root:

```sh
./burst.sh
```

Pass a base URL to hit a deployed service:

```sh
./burst.sh https://YOUR-SERVICE.example
```

The script creates a temporary show, sends 20,000 concurrent one-seat requests against 10 hot seats, then checks that `available + held + confirmed` equals the total. It requires Go and Bash. To change the load, run the Go burst program directly, for example `go run ./burst -url=http://localhost:8080 -c=20000 -s=10`.

## Health, metrics, and logs

- `GET /healthz` is liveness; `GET /readyz` checks PostgreSQL and returns 503 when it cannot be reached.
- `GET /metrics` exposes Prometheus metrics. Useful series include `http_requests_total`, `http_request_duration_seconds`, `reservations_held_total`, `reservations_confirmed_total`, `reservations_declined_total{reason=...}`, `reservations_idempotent_replays_total`, `seats_available{show_id=...}`, and `hold_reaper_failures_total` / `hold_reaper_last_success_timestamp_seconds`.
- The service writes request access logs, with request IDs, to stdout. Locally, read them with `docker compose logs -f app`; on a hosted deployment, use the host's container log viewer. For a remote Compose host, keep Prometheus bound to localhost and reach it through an SSH tunnel.

For a clean checkout, `docker compose up --build` builds the service image and runs the database schema initialization automatically. `go build ./...` also builds the API and burst program with Go 1.25 or newer.
