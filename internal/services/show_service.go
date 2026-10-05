package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"time"

	"bookingsystem/internal/metrics"
	"bookingsystem/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const userLimit = 4
const holdDurationMins = 15

type ShowService struct {
	db *pgxpool.Pool
}

func NewShowService(db *pgxpool.Pool) *ShowService {
	return &ShowService{db: db}
}

func (s *ShowService) Ping(ctx context.Context) error {
	return s.db.Ping(ctx)
}

func (s *ShowService) CreateShow(ctx context.Context, req models.ShowRequest) (string, error) {
	showID := uuid.New()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		log.Printf("DB error starting tx: %v", err)
		return "", err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, "INSERT INTO shows (id, name, price_paise, total_seats) VALUES ($1, $2, $3, $4)",
		showID, req.Name, req.PricePaise, len(req.Seats))
	if err != nil {
		log.Printf("DB error inserting show: %v", err)
		return "", err
	}

	for _, seat := range req.Seats {
		_, err = tx.Exec(ctx, "INSERT INTO seats (show_id, name, status) VALUES ($1, $2, 'available')", showID, seat)
		if err != nil {
			log.Printf("DB error inserting seat: %v", err)
			return "", err
		}
	}

	err = tx.Commit(ctx)
	if err != nil {
		log.Printf("Commit error: %v", err)
		return "", err
	}

	return showID.String(), nil
}

func (s *ShowService) GetShow(ctx context.Context, showID string) (map[string]int, map[string]string, error) {
	rows, err := s.db.Query(ctx, "SELECT name, status FROM seats WHERE show_id = $1", showID)
	if err != nil {
		log.Printf("DB error getting show: %v", err)
		return nil, nil, err
	}
	defer rows.Close()

	counts := map[string]int{"available": 0, "held": 0, "confirmed": 0}
	seats := map[string]string{}

	for rows.Next() {
		var name, status string
		if err := rows.Scan(&name, &status); err != nil {
			log.Printf("Scan error in getShow: %v", err)
			continue
		}
		counts[status]++
		seats[name] = status
	}

	return counts, seats, nil
}

type ReserveResult struct {
	ReservationID string
	Status        string
	AmountPaise   int64
	Seats         []string
	IsReplay      bool
}

func sameSeatSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	leftSorted := append([]string(nil), left...)
	rightSorted := append([]string(nil), right...)
	sort.Strings(leftSorted)
	sort.Strings(rightSorted)
	for i := range leftSorted {
		if leftSorted[i] != rightSorted[i] {
			return false
		}
	}
	return true
}

func (s *ShowService) ReserveSeats(ctx context.Context, showID, userID, idempotencyKey string, req models.ReserveRequest, reqBodyBytes []byte) (*ReserveResult, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		log.Printf("DB error starting tx in reserve: %v", err)
		return nil, fmt.Errorf("db_error")
	}
	defer tx.Rollback(ctx)

	// 1. Check/Insert Idempotency Key
	resID := uuid.New()
	tag, err := tx.Exec(ctx, "INSERT INTO idempotency_keys (key, show_id, user_id, request_body, reservation_id) VALUES ($1, $2, $3, $4, $5) ON CONFLICT (key) DO NOTHING",
		idempotencyKey, showID, userID, reqBodyBytes, resID)

	if err != nil {
		log.Printf("DB error inserting idempotency key: %v", err)
		return nil, fmt.Errorf("db_error")
	}

	if tag.RowsAffected() == 0 {
		// Unique violation occurred, fetch existing
		var existingResID uuid.UUID
		var existingBody []byte
		var existingShowID uuid.UUID
		var existingUserID string
		var existingAmount int64
		err = tx.QueryRow(ctx, "SELECT reservation_id, show_id, user_id, request_body, amount_paise FROM idempotency_keys WHERE key = $1", idempotencyKey).Scan(&existingResID, &existingShowID, &existingUserID, &existingBody, &existingAmount)
		if err != nil {
			log.Printf("DB error fetching idempotency key: %v", err)
			return nil, fmt.Errorf("db_error")
		}

		// Compare logically to avoid formatting/whitespace issues from JSONB
		var existingReq models.ReserveRequest
		if err := json.Unmarshal(existingBody, &existingReq); err != nil {
			log.Printf("Error unmarshaling existing body: %v", err)
			return nil, fmt.Errorf("db_error")
		}

		different := existingShowID.String() != showID || existingUserID != userID || !sameSeatSet(existingReq.Seats, req.Seats)

		if different {
			metrics.ReservationsDeclined.WithLabelValues("idempotent_replay").Inc()
			return nil, fmt.Errorf("idempotent_mismatch")
		}

		metrics.IdempotentReplays.Inc()

		return &ReserveResult{
			ReservationID: existingResID.String(),
			Status:        "held",
			AmountPaise:   existingAmount,
			Seats:         existingReq.Seats,
			IsReplay:      true,
		}, nil
	}

	var pricePaise int64
	err = tx.QueryRow(ctx, "SELECT price_paise FROM shows WHERE id = $1", showID).Scan(&pricePaise)
	if err != nil {
		log.Printf("DB error fetching show price: %v", err)
		return nil, fmt.Errorf("db_error")
	}

	// 2. Check User Limit using INSERT ... ON CONFLICT
	var newLimit int
	err = tx.QueryRow(ctx, `
		INSERT INTO user_show_limits (show_id, user_id, seats_booked)
		VALUES ($1, $2, $3)
		ON CONFLICT (show_id, user_id) DO UPDATE 
		SET seats_booked = user_show_limits.seats_booked + EXCLUDED.seats_booked
		WHERE user_show_limits.seats_booked + EXCLUDED.seats_booked <= $4
		RETURNING seats_booked
	`, showID, userID, len(req.Seats), userLimit).Scan(&newLimit)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			metrics.ReservationsDeclined.WithLabelValues("per_user_limit").Inc()
			return nil, fmt.Errorf("limit_exceeded")
		}
		log.Printf("DB error upserting user limit: %v", err)
		return nil, fmt.Errorf("db_error")
	}

	// Lock the requested rows in one deterministic order. Every reservation,
	// cancellation, and expiry path uses this order after locking the user quota.
	rows, err := tx.Query(ctx, `
		SELECT name
		FROM seats
		WHERE show_id = $1 AND name = ANY($2) AND status = 'available'
		ORDER BY name
		FOR UPDATE
	`, showID, req.Seats)
	if err != nil {
		log.Printf("DB error locking seats: %v", err)
		return nil, fmt.Errorf("db_error")
	}
	var lockedSeats []string
	for rows.Next() {
		var seat string
		if err := rows.Scan(&seat); err != nil {
			rows.Close()
			log.Printf("Scan error locking seats: %v", err)
			return nil, fmt.Errorf("db_error")
		}
		lockedSeats = append(lockedSeats, seat)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		log.Printf("Read error locking seats: %v", err)
		return nil, fmt.Errorf("db_error")
	}
	rows.Close()
	if len(lockedSeats) != len(req.Seats) {
		metrics.ReservationsDeclined.WithLabelValues("seat_taken").Inc()
		return nil, fmt.Errorf("seat_taken")
	}

	// The rows are locked in lexical name order before the state transition.
	rows, err = tx.Query(ctx, `
		UPDATE seats 
		SET status = 'held', user_id = $1, reservation_id = $2, updated_at = NOW()
		WHERE show_id = $3 AND name = ANY($4) AND status = 'available'
		RETURNING name
	`, userID, resID, showID, req.Seats)

	if err != nil {
		log.Printf("DB error reserving seats: %v", err)
		return nil, fmt.Errorf("db_error")
	}

	var updatedSeats []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			log.Printf("Scan error reserving seats: %v", err)
			continue
		}
		updatedSeats = append(updatedSeats, s)
	}
	rows.Close()

	if len(updatedSeats) != len(req.Seats) {
		metrics.ReservationsDeclined.WithLabelValues("seat_taken").Inc()
		return nil, fmt.Errorf("seat_taken")
	}

	amountPaise := pricePaise * int64(len(req.Seats))
	if _, err := tx.Exec(ctx, "UPDATE idempotency_keys SET amount_paise = $1 WHERE key = $2", amountPaise, idempotencyKey); err != nil {
		log.Printf("DB error saving idempotency response: %v", err)
		return nil, fmt.Errorf("db_error")
	}

	if err := tx.Commit(ctx); err != nil {
		log.Printf("Commit error: %v", err)
		return nil, fmt.Errorf("db_error")
	}

	metrics.ReservationsHeld.Inc()

	return &ReserveResult{
		ReservationID: resID.String(),
		Status:        "held", // Now it's initially held
		AmountPaise:   amountPaise,
		Seats:         append([]string(nil), req.Seats...),
		IsReplay:      false,
	}, nil
}

func (s *ShowService) ConfirmReservation(ctx context.Context, resID, userID string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		log.Printf("DB error starting tx in confirm: %v", err)
		return fmt.Errorf("db_error")
	}
	defer tx.Rollback(ctx)

	lockRows, err := tx.Query(ctx, `
		SELECT name
		FROM seats
		WHERE reservation_id = $1 AND user_id = $2 AND status = 'held'
			AND updated_at > NOW() - ($3::int * INTERVAL '1 minute')
		ORDER BY name
		FOR UPDATE
	`, resID, userID, holdDurationMins)
	if err != nil {
		log.Printf("DB error locking seats in confirm: %v", err)
		return fmt.Errorf("db_error")
	}
	lockedNames := make([]string, 0, 4)
	for lockRows.Next() {
		var name string
		if err := lockRows.Scan(&name); err != nil {
			lockRows.Close()
			log.Printf("Scan error locking seats in confirm: %v", err)
			return fmt.Errorf("db_error")
		}
		lockedNames = append(lockedNames, name)
	}
	if err := lockRows.Err(); err != nil {
		lockRows.Close()
		log.Printf("Read error locking seats in confirm: %v", err)
		return fmt.Errorf("db_error")
	}
	lockRows.Close()
	if len(lockedNames) == 0 {
		return fmt.Errorf("not_found_or_expired")
	}

	rows, err := tx.Query(ctx, `
		UPDATE seats 
		SET status = 'confirmed', updated_at = NOW()
		WHERE reservation_id = $1 AND user_id = $2 AND name = ANY($3) AND status = 'held'
			AND updated_at > NOW() - ($4::int * INTERVAL '1 minute')
		RETURNING name
	`, resID, userID, lockedNames, holdDurationMins)
	if err != nil {
		log.Printf("DB error updating seats in confirm: %v", err)
		return fmt.Errorf("db_error")
	}

	var confirmedSeats int
	for rows.Next() {
		confirmedSeats++
	}
	rows.Close()

	if confirmedSeats != len(lockedNames) {
		return fmt.Errorf("not_found_or_expired")
	}

	if err := tx.Commit(ctx); err != nil {
		log.Printf("Commit error in confirm: %v", err)
		return fmt.Errorf("db_error")
	}
	metrics.ReservationsConfirmed.Inc()

	return nil
}

func (s *ShowService) CancelReservation(ctx context.Context, resID, userID string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		log.Printf("DB error starting tx in cancel: %v", err)
		return fmt.Errorf("db_error")
	}
	defer tx.Rollback(ctx)

	// Cancellation locks the quota row before seat rows, matching reservation
	// and hold-expiry lock ordering.
	var showID string
	err = tx.QueryRow(ctx, `
		SELECT show_id::text
		FROM idempotency_keys
		WHERE reservation_id = $1 AND user_id = $2
	`, resID, userID).Scan(&showID)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("not_found")
	}
	if err != nil {
		log.Printf("DB error finding reservation in cancel: %v", err)
		return fmt.Errorf("db_error")
	}
	var seatsBooked int
	err = tx.QueryRow(ctx, `
		SELECT seats_booked
		FROM user_show_limits
		WHERE show_id = $1 AND user_id = $2
		FOR UPDATE
	`, showID, userID).Scan(&seatsBooked)
	if err != nil {
		log.Printf("DB error locking user quota in cancel: %v", err)
		return fmt.Errorf("db_error")
	}
	lockRows, err := tx.Query(ctx, `
		SELECT name
		FROM seats
		WHERE reservation_id = $1 AND user_id = $2 AND status IN ('held', 'confirmed')
		ORDER BY name
		FOR UPDATE
	`, resID, userID)
	if err != nil {
		log.Printf("DB error locking seats in cancel: %v", err)
		return fmt.Errorf("db_error")
	}
	lockedNames := make([]string, 0, 4)
	for lockRows.Next() {
		var name string
		if err := lockRows.Scan(&name); err != nil {
			lockRows.Close()
			log.Printf("Scan error locking seats in cancel: %v", err)
			return fmt.Errorf("db_error")
		}
		lockedNames = append(lockedNames, name)
	}
	if err := lockRows.Err(); err != nil {
		lockRows.Close()
		log.Printf("Read error locking seats in cancel: %v", err)
		return fmt.Errorf("db_error")
	}
	lockRows.Close()
	if len(lockedNames) == 0 {
		return fmt.Errorf("not_found")
	}

	rows, err := tx.Query(ctx, `
		UPDATE seats 
		SET status = 'available', user_id = NULL, reservation_id = NULL
		WHERE reservation_id = $1 AND user_id = $2 AND name = ANY($3)
			AND status IN ('held', 'confirmed')
		RETURNING name
	`, resID, userID, lockedNames)
	if err != nil {
		log.Printf("DB error updating seats in cancel: %v", err)
		return fmt.Errorf("db_error")
	}

	var canceledSeats int
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			log.Printf("Scan error in cancel: %v", err)
			continue
		}
		canceledSeats++
	}
	rows.Close()

	if canceledSeats == 0 {
		return fmt.Errorf("not_found")
	}

	// Update user limit
	commandTag, err := tx.Exec(ctx, `
		UPDATE user_show_limits 
		SET seats_booked = seats_booked - $1 
		WHERE show_id = $2 AND user_id = $3 AND seats_booked >= $1
	`, canceledSeats, showID, userID)
	if err != nil {
		log.Printf("DB error updating user limits in cancel: %v", err)
		return fmt.Errorf("db_error")
	}
	if commandTag.RowsAffected() != 1 {
		log.Printf("DB invariant error: quota underflow for show %s user %s", showID, userID)
		return fmt.Errorf("db_error")
	}

	if err := tx.Commit(ctx); err != nil {
		log.Printf("Commit error in cancel: %v", err)
		return fmt.Errorf("db_error")
	}

	return nil
}

func (s *ShowService) StartHoldReaper(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.reapExpiredHolds()
			}
		}
	}()
}

func (s *ShowService) reapExpiredHolds() {
	success := false
	defer func() {
		if success {
			metrics.HoldReaperLastSuccess.SetToCurrentTime()
		} else {
			metrics.HoldReaperFailures.Inc()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. Identify expired holds and their counts
	// 2. Free the seats
	// 3. Decrement user limits

	tx, err := s.db.Begin(ctx)
	if err != nil {
		log.Printf("Reaper: failed to start tx: %v", err)
		return
	}
	defer tx.Rollback(ctx)

	// Get candidate quota rows, then lock each quota before its seats. A cancel
	// or confirmation racing this scan is rechecked when the seat rows are locked.
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT show_id, user_id
		FROM seats
		WHERE status = 'held' AND updated_at <= NOW() - ($1::int * INTERVAL '1 minute')
		ORDER BY show_id, user_id
	`, holdDurationMins)
	if err != nil {
		log.Printf("Reaper: failed to query expired holds: %v", err)
		return
	}

	type expiredData struct {
		showID string
		userID string
	}
	var expiredList []expiredData

	for rows.Next() {
		var d expiredData
		if err := rows.Scan(&d.showID, &d.userID); err != nil {
			log.Printf("Reaper: scan error: %v", err)
			continue
		}
		expiredList = append(expiredList, d)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		log.Printf("Reaper: failed reading expired holds: %v", err)
		return
	}
	rows.Close()

	if len(expiredList) == 0 {
		success = true
		return // nothing to do
	}

	totalExpired := 0
	for _, d := range expiredList {
		var booked int
		if err := tx.QueryRow(ctx, `
			SELECT seats_booked
			FROM user_show_limits
			WHERE show_id = $1 AND user_id = $2
			FOR UPDATE
		`, d.showID, d.userID).Scan(&booked); err != nil {
			log.Printf("Reaper: failed to lock quota for show %s user %s: %v", d.showID, d.userID, err)
			return
		}

		seatRows, err := tx.Query(ctx, `
			SELECT name
			FROM seats
			WHERE show_id = $1 AND user_id = $2 AND status = 'held'
				AND updated_at <= NOW() - ($3::int * INTERVAL '1 minute')
			ORDER BY name
			FOR UPDATE
		`, d.showID, d.userID, holdDurationMins)
		if err != nil {
			log.Printf("Reaper: failed to lock expired seats for show %s user %s: %v", d.showID, d.userID, err)
			return
		}
		var names []string
		for seatRows.Next() {
			var name string
			if err := seatRows.Scan(&name); err != nil {
				seatRows.Close()
				log.Printf("Reaper: failed to scan expired seat: %v", err)
				return
			}
			names = append(names, name)
		}
		if err := seatRows.Err(); err != nil {
			seatRows.Close()
			log.Printf("Reaper: failed to read expired seats: %v", err)
			return
		}
		seatRows.Close()
		if len(names) == 0 {
			continue
		}

		freed, err := tx.Exec(ctx, `
			UPDATE seats
			SET status = 'available', user_id = NULL, reservation_id = NULL
			WHERE show_id = $1 AND user_id = $2 AND name = ANY($3)
				AND status = 'held'
		`, d.showID, d.userID, names)
		if err != nil {
			log.Printf("Reaper: failed to free expired seats: %v", err)
			return
		}
		count := int(freed.RowsAffected())
		if count == 0 {
			continue
		}
		quotaUpdate, err := tx.Exec(ctx, `
			UPDATE user_show_limits
			SET seats_booked = seats_booked - $1
			WHERE show_id = $2 AND user_id = $3
				AND seats_booked >= $1
		`, count, d.showID, d.userID)
		if err != nil {
			log.Printf("Reaper: failed to update limit for user %s: %v", d.userID, err)
			return
		}
		if quotaUpdate.RowsAffected() != 1 {
			log.Printf("Reaper: quota underflow for show %s user %s", d.showID, d.userID)
			return
		}
		totalExpired += count
	}

	if err := tx.Commit(ctx); err != nil {
		log.Printf("Reaper: commit failed: %v", err)
	} else {
		success = true
		log.Printf("Reaper: successfully freed %d expired seats", totalExpired)
	}
}
