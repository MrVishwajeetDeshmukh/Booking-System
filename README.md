# Seat Reservation Service

A Go and PostgreSQL API for creating shows, placing temporary seat holds, confirming or cancelling reservations, and exposing Prometheus metrics. Prices are integer paise end to end.

## Run locally

Requirements: Go 1.25 or newer, Docker, and Docker Compose. Copy the local-only environment sample and start the same container stack used for deployment:

```sh
cp .env.example .env
docker compose up --build -d
```

The first start initializes PostgreSQL from `db/schema.sql`. Check readiness and logs:

```sh
curl http://localhost:8080/readyz
docker compose logs -f app
```

Prometheus is at <http://localhost:9090>; the service scrape endpoint is <http://localhost:8080/metrics>. The database and Prometheus UI bind only to localhost. The persistent `postgres_data` volume survives container restarts. `docker compose down` stops the stack and preserves that volume.

`AUTH_TOKEN_SECRET` must be at least 32 bytes. The committed `.env.example` value is for local development only; set a cryptographically random secret in the deployment platform's secret settings. `POST /auth/register` creates a server-assigned anonymous user ID and returns a signed HS256 bearer token that expires after 24 hours. Reservation, confirmation, and cancellation use the bearer token's `sub` as identity. A request body or `X-User-ID` header cannot select another user.

## Deploy to Render

The checked-in `render.yaml` is a Render Blueprint for the Docker web service and PostgreSQL database. Push this repository to a public GitHub repo, then in Render choose **New → Blueprint**, connect that repo, and apply the Blueprint. Render generates `AUTH_TOKEN_SECRET` and connects `DATABASE_URL`; the service initializes the schema from `db/schema.sql` on startup. Once healthy, the public API URL is shown on the Render service page. Use that base URL for the burst script and the `/metrics` endpoint. Container logs are available in the service's Logs tab.

This Blueprint uses Render's free web and database plans for assignment review. Those plans have constraints: the web service spins down after 15 minutes idle and runs as one instance; the free database expires after 30 days and has no backups. Treat it as a short-lived review deployment, not production infrastructure. See [Render's free-plan limitations](https://render.com/docs/free).

## API quick start

Create a show and note the returned `id`:

```sh
curl -X POST http://localhost:8080/shows \
  -H 'Content-Type: application/json' \
  -d '{"name":"Friday show","price_paise":25000,"seats":["A0","A1","A2","A3","A4","A5","A6","A7","A8","A9"]}'
```

Register an anonymous user and copy the returned token:

```sh
curl -X POST http://localhost:8080/auth/register
```

Reserve a seat. A successful reservation creates a 15-minute hold; use its `reservation_id` to confirm or cancel it.

```sh
curl -X POST http://localhost:8080/shows/SHOW_ID/reserve \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer TOKEN' \
  -H 'Idempotency-Key: request-123' \
  -d '{"seats":["A0"]}'

curl -X POST http://localhost:8080/reservations/RESERVATION_ID/confirm \
  -H 'Authorization: Bearer TOKEN'
```

Show creation is left open in this take-home service so reviewers can bootstrap shows; protect it with an admin role before using the API for real events. User IDs are server-assigned, tokens are signed and time-limited, and reservation ownership always comes from the bearer token.

## One-command burst

With the local stack running, run the included script from the repository root:

```sh
./burst.sh
```

Pass a base URL to hit a deployed service:

```sh
./burst.sh https://YOUR-SERVICE.example
```

The script registers distinct bearer-token users, sends 20,000 concurrent requests for the same hot seat, then checks concurrent same-key retries, same-key/different-body rejection, and ten parallel distinct-seat requests from one user. It reports held responses, declines by reason, 5xx and transport errors, verifies `available + held + confirmed == total_seats`, and checks that the Prometheus `seats_available` gauge matches the API state. It requires Go and Bash. To change the load, run `go run ./burst -url=http://localhost:8080 -c=20000 -u=5000 -s=12` (at least 12 seats are needed for every scenario).

## Health, metrics, and logs

- `GET /healthz` is liveness; `GET /readyz` checks PostgreSQL and returns 503 when it cannot be reached.
- `GET /metrics` exposes Prometheus metrics, including `http_requests_total`, `http_request_duration_seconds`, `reservations_held_total`, `reservations_confirmed_total`, `reservations_declined_total{reason=...}`, `reservations_idempotent_replays_total`, `seats_available{show_id=...}`, `hold_reaper_failures_total`, and `hold_reaper_last_success_timestamp_seconds`. The available-seat gauge is read from PostgreSQL at scrape time so it reconciles with the source of truth.
- Request access logs go to stdout and include a generated request ID, status, latency, method, path, and error. Locally, use `docker compose logs -f app`; on a hosted deployment, use the platform's container log viewer. For a remote Compose host, keep Prometheus bound to localhost and access it through an SSH tunnel.

A clean checkout builds with `go build ./...` and runs with `docker compose up --build`. The app also applies the idempotent schema statements at startup, so the Render Blueprint can initialize its new database.
