# Assignment write-up

## Atomic decision and lock order

The seat claim is one PostgreSQL transaction. It first locks the `(show_id, user_id)` quota row through an `INSERT ... ON CONFLICT DO UPDATE ... WHERE seats_booked + requested <= 4`. PostgreSQL's unique-key conflict handling serializes concurrent updates to that quota row, so two requests from one user cannot both pass the four-seat limit based on a stale read.

For the inventory decision, the service runs `SELECT ... WHERE status = 'available' ORDER BY name FOR UPDATE`, checks that it locked every requested seat, then updates those locked rows to `held` and commits. The final update is also conditional on `status = 'available'` and returns the changed rows. Under PostgreSQL `READ COMMITTED`, a competing updater waits for the row lock and rechecks the predicate against the committed row; only one transaction can transition a given seat from available to held. If any requested seat is absent or no longer available, the transaction rolls back, including its quota increment.

Multi-seat requests lock seat rows in lexical seat-name order, regardless of the caller's input order. Reservation, cancellation, and expiry take the user's quota row before locking seats; expiry walks `(show_id, user_id)` groups in database order. Confirmation only locks and changes seat rows and never waits for a quota row. This gives competing seat-changing transactions a consistent quota-before-seat order and a consistent order among multiple seat rows, avoiding a cycle where transactions hold different requested seats while waiting on each other.

## Idempotency

`idempotency_keys` stores the key as its primary key, plus the show ID, user ID, JSONB request, reservation ID, and original integer-paise amount. The key insert, quota increment, seat hold, and stored response amount commit in the same transaction. A failed or partial reservation rolls all of them back. A concurrent retry waits on the unique key; after the first transaction commits, it reads and returns the original reservation result rather than creating another hold.

Replay comparison binds the key to the same show, user, and seat set (seat order is immaterial). Reusing a key for another user, show, or seat set returns 409. A valid replay returns the original 201 response values, including the original hold status and amount. Keys are retained indefinitely in this version. The user ID is trusted from `X-User-ID`; production authentication and ownership identity must be supplied by a trusted gateway.

## Holds and expiry

A successful reservation starts in `held`. It can be confirmed by its owner before expiry. A worker checks for holds older than 15 minutes every 30 seconds, then in one transaction locks each affected user's quota and the matching seat rows, returns the still-expired seats to available, and decrements quota by the number of seats actually released. If a concurrent confirmation or cancellation wins the seat lock, the expiry query rechecks the status and does not decrement quota for seats it did not release. Cancellation supports both held and confirmed reservations.

## Consistency under a partition

PostgreSQL is the single source of truth; there is no inventory cache or write-behind path. If the service cannot reach PostgreSQL, readiness returns 503 and reservation operations cannot commit. The service gives up write availability during that failure rather than accepting reservations against stale inventory. The API is designed for one writable PostgreSQL primary; it does not implement multi-region failover.

## Observability and paging

Access logs go to stdout and include a generated request ID, HTTP status, latency, method, path, and error. Prometheus is exposed at `/metrics`; it has request count and duration by route, held/confirmed/declined/replay counters, an available-seat gauge refreshed from PostgreSQL every 15 seconds, and hold-reaper failure and last-success metrics.

At 2am I would page on sustained readiness failures or database connection failures, a sharp increase in 5xx responses, p99 latency above the service objective, an increasing `hold_reaper_failures_total` or stale `hold_reaper_last_success_timestamp_seconds`, and a database reconciliation showing `available + held + confirmed != total_seats` or a negative quota. I would investigate a surge in 409 conflicts as a possible demand spike, but would not page on expected seat contention by itself.

## AI usage

This deployment-readiness pass used OpenAI Codex to review the transaction and deployment paths, update the implementation and documentation, and prepare build/run instructions. I directed the work toward deploy readiness and the assignment's stated guarantees. Codex identified the need for deterministic multi-seat locks and user/show-bound idempotency, and proposed the implementation details now in the service. The repository's earlier write-up recorded Gemini 3.1 Pro assistance for the initial API, concurrency, and metrics work; this checkout contains only one baseline commit, so it does not provide a commit-by-commit record of that earlier collaboration.

## What I would do next

Add database-backed integration tests for same-seat races, opposite-order multi-seat requests, duplicate idempotency requests, same-key body mismatch, expiry racing confirm/cancel, and quota reconciliation. Add schema migrations and a supported retention policy for idempotency records. Replace the assignment's `X-User-ID` header convention with authenticated identity, add rate limits, and run the service behind TLS. Establish production alert thresholds from load data and make database backups and restore drills part of deployment operations.
