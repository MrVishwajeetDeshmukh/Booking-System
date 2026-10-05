# Assignment write-up

## Atomic decision and lock order

The seat claim is one PostgreSQL transaction. It first updates the `(show_id, user_id)` quota row through `INSERT ... ON CONFLICT DO UPDATE ... WHERE seats_booked + requested <= 4`. PostgreSQL's unique-key conflict handling serializes updates to that quota row, so concurrent requests from one user cannot pass the limit from a stale read.

For inventory, the service runs `SELECT ... WHERE status = 'available' ORDER BY name FOR UPDATE`, checks that it locked every requested seat, then updates those rows to `held` with a second `status = 'available'` predicate and commits. Under PostgreSQL `READ COMMITTED`, a competing updater waits for the row lock and rechecks its predicate against the committed row; only one transaction can transition a seat from available to held. If any requested seat is absent or unavailable, the transaction rolls back, including its quota increment. Multi-seat requests are all-or-nothing.

Reservation, cancellation, and expiry take the user's quota row before seat rows. Reservation, confirmation, cancellation, and expiry lock any multiple seat rows in lexical seat-name order, independent of input order. Expiry processes `(show_id, user_id)` groups in database order. Confirmation does not lock a quota row, so it cannot form the reverse edge of a quota/seat wait cycle. This is the lock order that prevents two multi-seat transactions from each holding a different seat while waiting for the other.

## Identity and idempotency

`POST /auth/register` creates a random server-assigned user ID and returns a 24-hour HS256 bearer token. The API verifies its signature, issuer, audience, and expiry; reservation, confirmation, and cancellation take identity only from the token's `sub`. A body field or `X-User-ID` header cannot override it. Registration is open and anonymous for the take-home evaluator; it proves possession of a server-issued token, not a real-world identity. `POST /shows` is also open so reviewers can create test inventory.

`idempotency_keys` stores the key as its primary key, plus the show ID, token-derived user ID, JSONB request, reservation ID, and original amount in paise. The key insert, quota increment, seat hold, and response amount commit in the same transaction. Failed reservations roll all of them back. A concurrent retry waits on the unique key and then returns the original reservation response instead of creating another hold.

Replay comparison binds the key to the same show, user, and seat set (seat order is immaterial). Reusing it for another user, show, or seat set returns 409 and increments `reservations_declined_total{reason="idempotent_replay"}`. A valid replay returns the original 201 values and increments `reservations_idempotent_replays_total`. Keys are retained indefinitely in this version.

## Holds and expiry

A successful reservation starts in `held`. Its owner can confirm it within 15 minutes; confirmation checks the timestamp in PostgreSQL, not just whether the background worker has run. A worker looks for expired holds every 30 seconds, then in one transaction locks each affected user's quota and sorted matching seat rows, releases only rows that remain expired and held, and decrements quota by the number actually released. If confirmation or cancellation wins a row lock, expiry rechecks the status and does not release or decrement it. Cancellation supports held and confirmed reservations.

## Consistency under a partition

PostgreSQL is the single source of truth; there is no inventory cache or write-behind path. If the service cannot reach PostgreSQL, readiness returns 503 and reservation writes cannot commit. The service gives up write availability rather than accepting decisions against stale inventory. The deployment expects one writable PostgreSQL primary and does not implement multi-region failover.

## Observability and paging

Access logs go to stdout and include a generated request ID, HTTP status, latency, method, path, and error. Prometheus exposes HTTP request counts and duration, held/confirmed/declined/idempotent-replay counters, the hold reaper's failure and last-success metrics, and `seats_available`. That gauge queries PostgreSQL at scrape time, so it reflects the database state rather than process-local deltas.

At 2am I would page on sustained readiness failures or database connection failures, a sharp increase in 5xx responses, p99 latency above the service objective, an increasing `hold_reaper_failures_total` or stale `hold_reaper_last_success_timestamp_seconds`, and database reconciliation showing `available + held + confirmed != total_seats` or a negative quota. I would investigate a surge in 409 conflicts as a possible demand spike but would not page on expected seat contention alone.

## AI usage

The repository's original write-up records Gemini 3.1 Pro assistance on the initial API, concurrency, and metrics work. In this deployment-readiness pass, I used OpenAI Codex to review and change the transaction paths, add signed-token identity, build the hot-seat/idempotency/per-user burst program, and prepare the container and operations docs. I directed the work toward the assignment checks; Codex identified and proposed the deterministic locking, token verification, and burst scenarios. I reviewed the resulting code and build output. The checkout originally contained one baseline commit; the follow-up commits record this pass incrementally, but cannot reconstruct the earlier development history.

## What I would do next

Add database-backed integration tests for hot-seat races, opposite-order multi-seat requests, duplicate idempotency requests, mismatched keys, expiry racing confirm/cancel, and quota reconciliation. Protect show creation with an admin role, replace anonymous registration with a trusted identity provider, rate-limit token issuance, and add a retention policy and schema migrations. Establish production alert thresholds from load data, then automate backups and restore drills.
