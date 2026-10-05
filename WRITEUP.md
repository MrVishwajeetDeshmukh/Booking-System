# Seat Reservation System at Scale

## 1. Atomic Decision & Concurrency
The core requirement is to never double-sell a seat under high concurrency.
A "read-then-write" approach leads to race conditions. Instead, this system uses a **conditional update guarded on current state**.

When reserving seats, the system runs:
```sql
UPDATE seats 
SET status = 'confirmed', user_id = $1, reservation_id = $2
WHERE show_id = $3 AND name = ANY($4) AND status = 'available'
RETURNING name;
```
Because PostgreSQL uses row-level locks on `UPDATE`, multiple concurrent updates targeting the same hot seat will queue up sequentially in the database engine. The first one to execute changes the state to `confirmed`. Subsequent queued updates will then fail to find rows matching `status = 'available'` and return 0 rows, prompting the Go application to gracefully decline the request with a `409 Conflict` (Seat taken).

**Deadlock Avoidance**: For multi-seat requests, we avoid deadlocks by performing a single bulk `UPDATE ... WHERE name = ANY(...)` rather than issuing separate individual updates. Postgres processes this internally as a single atomic operation, locking the rows safely.

## 2. Idempotency & Per-User Limits
### Idempotency
The idempotency key is stored in a dedicated `idempotency_keys` PostgreSQL table with a `UNIQUE` constraint on the key. 
- The system attempts an `INSERT ... ON CONFLICT DO NOTHING`.
- If the insert succeeds, it's a new request and proceeds to book.
- If no rows are affected (conflict), it's a replay. It fetches the saved request body and does a logical JSON comparison against the incoming body.
- **Same key, different body**: Rejected with `409 Conflict`.
- **Same key, same body**: Returns the original `reservation_id` along with its *actual, current status* in the database (e.g., if you successfully booked it but then cancelled it, a replay will tell you it is now `cancelled`).

### Per-User Limit (Concurrency-Safe)
To enforce the 4-seat limit concurrently without suffering from phantom reads, the system uses an Upsert into a `user_show_limits` table:
```sql
INSERT INTO user_show_limits (show_id, user_id, seats_booked) VALUES ($1, $2, $3)
ON CONFLICT (show_id, user_id) DO UPDATE 
SET seats_booked = user_show_limits.seats_booked + EXCLUDED.seats_booked
WHERE user_show_limits.seats_booked + EXCLUDED.seats_booked <= 4
RETURNING seats_booked;
```
If the limit is exceeded, the `WHERE` condition fails, returning 0 rows (`pgx.ErrNoRows`), allowing us to atomic decline with `409 Conflict`.

## 3. Holds & Expiry
This system implements the **explicit cancellation** model (POST `/reservations/{id}/cancel`). Only the original owner (identified by `X-User-ID`) can cancel the reservation. Canceling cleanly wipes the `reservation_id` and `user_id` from the seats, returns them to `available`, and decrements the user's quota in `user_show_limits`.

## 4. Consistency vs Availability under a Partition
The system relies on a single relational database (PostgreSQL) as the sole source of truth, choosing **Consistency (C)** over Availability (A) in the CAP theorem. If the database is unreachable, the API's readiness check (`/readyz`) fails, and the system fails closed (503 Service Unavailable). This is strictly required for financial/ticketing systems where over-selling or phantom inventory is absolutely unacceptable.

## 5. Observability
Structured logs are generated per request, injected with unique Request IDs for correlation.
Prometheus metrics are exposed at `/metrics`:
- `reservations_confirmed_total` (counter)
- `reservations_declined_total` (counter, partitioned by reason: `seat_taken`, `per_user_limit`, `idempotent_replay`)
- `seats_available` (gauge)

**2 AM Pages**: I would want to be paged for:
- Elevated `5xx` error rates (indicates database unreachable or application panics).
- Anomaly in `seats_available` gauge (if it drops below 0, or if `available + confirmed != total`).
- Latency spikes (p99 > 500ms) which could indicate database lock contention.

## 6. AI Usage
AI (Gemini 3.1 Pro) was used collaboratively via an agentic IDE. 
- **Directed**: I commanded the AI to build an API using Go and PostgreSQL.
- **Decided**: The AI autonomously selected the specific SQL mechanisms (like `ON CONFLICT DO UPDATE ... WHERE` for the limit check, and `ON CONFLICT DO NOTHING` to fix transaction abort states on idempotency conflicts) to ensure absolute correctness under load. The AI also wrote the concurrent burst testing script and Prometheus middleware.

## 7. What's Next
- Move holds to a time-boxed TTL model (e.g., reserving changes status to `held`, and a background worker clears it back to `available` if payment is not confirmed within 10 minutes).
- Implement database connection pooling limits optimized for the specific hardware to prevent connection exhaustion.
- Add comprehensive unit testing for edge cases.

## Running the Burst Test
```bash
# Make sure the script is executable
chmod +x burst.sh

# Run against local
./burst.sh http://localhost:8080

# Run against production
./burst.sh https://your-production-url.com
```
