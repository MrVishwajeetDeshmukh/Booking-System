package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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

	metrics.SeatsAvailable.WithLabelValues(showID.String()).Set(float64(len(req.Seats)))

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
	AmountPaise   int
	IsReplay      bool
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
	tag, err := tx.Exec(ctx, "INSERT INTO idempotency_keys (key, user_id, request_body, reservation_id) VALUES ($1, $2, $3, $4) ON CONFLICT (key) DO NOTHING",
		idempotencyKey, userID, reqBodyBytes, resID)

	if err != nil {
		log.Printf("DB error inserting idempotency key: %v", err)
		return nil, fmt.Errorf("db_error")
	}

	if tag.RowsAffected() == 0 {
		// Unique violation occurred, fetch existing
		var existingResID uuid.UUID
		var existingBody []byte
		err = tx.QueryRow(ctx, "SELECT reservation_id, request_body FROM idempotency_keys WHERE key = $1", idempotencyKey).Scan(&existingResID, &existingBody)
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

		different := len(existingReq.Seats) != len(req.Seats)
		if !different {
			for i := range existingReq.Seats {
				if existingReq.Seats[i] != req.Seats[i] {
					different = true
					break
				}
			}
		}

		if different {
			metrics.ReservationsDeclined.WithLabelValues("idempotent_replay").Inc()
			return nil, fmt.Errorf("idempotent_mismatch")
		}

		// Check if it's still active or if it was cancelled
		var activeCount int
		err = tx.QueryRow(ctx, "SELECT COUNT(*) FROM seats WHERE reservation_id = $1 AND status != 'available'", existingResID).Scan(&activeCount)

		status := "held"
		if err == nil && activeCount == 0 {
			status = "cancelled_or_expired"
		} else if err == nil && activeCount > 0 {
			// check exact status of first seat
			var seatStatus string
			tx.QueryRow(ctx, "SELECT status FROM seats WHERE reservation_id = $1 LIMIT 1", existingResID).Scan(&seatStatus)
			status = seatStatus
		}

		return &ReserveResult{
			ReservationID: existingResID.String(),
			Status:        status,
			IsReplay:      true,
		}, nil
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

	// 3. Atomically Reserve (HOLD) Seats
	rows, err := tx.Query(ctx, `
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

	// Fetch price to return
	var pricePaise int
	err = tx.QueryRow(ctx, "SELECT price_paise FROM shows WHERE id = $1", showID).Scan(&pricePaise)
	if err != nil {
		pricePaise = 0
	}

	if err := tx.Commit(ctx); err != nil {
		log.Printf("Commit error: %v", err)
		return nil, fmt.Errorf("db_error")
	}

	metrics.ReservationsConfirmed.Inc()
	metrics.SeatsAvailable.WithLabelValues(showID).Sub(float64(len(req.Seats)))

	return &ReserveResult{
		ReservationID: resID.String(),
		Status:        "held", // Now it's initially held
		AmountPaise:   pricePaise * len(req.Seats),
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

	rows, err := tx.Query(ctx, `
		UPDATE seats 
		SET status = 'confirmed', updated_at = NOW()
		WHERE reservation_id = $1 AND user_id = $2 AND status = 'held'
		RETURNING name
	`, resID, userID)
	if err != nil {
		log.Printf("DB error updating seats in confirm: %v", err)
		return fmt.Errorf("db_error")
	}

	var confirmedSeats int
	for rows.Next() {
		confirmedSeats++
	}
	rows.Close()

	if confirmedSeats == 0 {
		return fmt.Errorf("not_found_or_expired")
	}

	if err := tx.Commit(ctx); err != nil {
		log.Printf("Commit error in confirm: %v", err)
		return fmt.Errorf("db_error")
	}

	return nil
}

func (s *ShowService) CancelReservation(ctx context.Context, resID, userID string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		log.Printf("DB error starting tx in cancel: %v", err)
		return fmt.Errorf("db_error")
	}
	defer tx.Rollback(ctx)

	// Can cancel either 'held' or 'confirmed'
	rows, err := tx.Query(ctx, `
		UPDATE seats 
		SET status = 'available', user_id = NULL, reservation_id = NULL
		WHERE reservation_id = $1 AND user_id = $2 AND status IN ('held', 'confirmed')
		RETURNING show_id::text, name
	`, resID, userID)
	if err != nil {
		log.Printf("DB error updating seats in cancel: %v", err)
		return fmt.Errorf("db_error")
	}

	var showID string
	var canceledSeats int
	for rows.Next() {
		var name string
		if err := rows.Scan(&showID, &name); err != nil {
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
	_, err = tx.Exec(ctx, `
		UPDATE user_show_limits 
		SET seats_booked = seats_booked - $1 
		WHERE show_id = $2 AND user_id = $3
	`, canceledSeats, showID, userID)
	if err != nil {
		log.Printf("DB error updating user limits in cancel: %v", err)
		return fmt.Errorf("db_error")
	}

	if err := tx.Commit(ctx); err != nil {
		log.Printf("Commit error in cancel: %v", err)
		return fmt.Errorf("db_error")
	}

	metrics.SeatsAvailable.WithLabelValues(showID).Add(float64(canceledSeats))
	return nil
}

func (s *ShowService) StartHoldReaper(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	go func() {
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

	// Fetch expired counts
	rows, err := tx.Query(ctx, `
		SELECT show_id, user_id, COUNT(*) as seats_expired
		FROM seats
		WHERE status = 'held' AND updated_at < NOW() - INTERVAL '15 minutes'
		GROUP BY show_id, user_id
	`)
	if err != nil {
		log.Printf("Reaper: failed to query expired holds: %v", err)
		return
	}

	type expiredData struct {
		showID string
		userID string
		count  int
	}
	var expiredList []expiredData

	for rows.Next() {
		var d expiredData
		if err := rows.Scan(&d.showID, &d.userID, &d.count); err != nil {
			log.Printf("Reaper: scan error: %v", err)
			continue
		}
		expiredList = append(expiredList, d)
	}
	rows.Close()

	if len(expiredList) == 0 {
		return // nothing to do
	}

	// Update seats
	_, err = tx.Exec(ctx, `
		UPDATE seats
		SET status = 'available', user_id = NULL, reservation_id = NULL
		WHERE status = 'held' AND updated_at < NOW() - INTERVAL '15 minutes'
	`)
	if err != nil {
		log.Printf("Reaper: failed to free seats: %v", err)
		return
	}

	// Update limits
	for _, d := range expiredList {
		_, err = tx.Exec(ctx, `
			UPDATE user_show_limits
			SET seats_booked = seats_booked - $1
			WHERE show_id = $2 AND user_id = $3
		`, d.count, d.showID, d.userID)
		if err != nil {
			log.Printf("Reaper: failed to update limit for user %s: %v", d.userID, err)
		}
		metrics.SeatsAvailable.WithLabelValues(d.showID).Add(float64(d.count))
	}

	if err := tx.Commit(ctx); err != nil {
		log.Printf("Reaper: commit failed: %v", err)
	} else {
		log.Printf("Reaper: successfully freed %d expired holds batches", len(expiredList))
	}
}
