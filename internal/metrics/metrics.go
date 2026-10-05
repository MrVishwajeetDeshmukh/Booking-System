package metrics

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type availableSeatsCollector struct {
	db   *pgxpool.Pool
	desc *prometheus.Desc
}

func RegisterAvailableSeatsCollector(db *pgxpool.Pool) error {
	collector := &availableSeatsCollector{
		db: db,
		desc: prometheus.NewDesc(
			"seats_available",
			"Number of currently available seats, read from PostgreSQL at scrape time",
			[]string{"show_id"}, nil,
		),
	}
	return prometheus.Register(collector)
}

func (c *availableSeatsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.desc
}

func (c *availableSeatsCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rows, err := c.db.Query(ctx, `
		SELECT shows.id::text, COUNT(seats.name) FILTER (WHERE seats.status = 'available')
		FROM shows
		LEFT JOIN seats ON seats.show_id = shows.id
		GROUP BY shows.id
	`)
	if err != nil {
		log.Printf("Metrics: failed to collect available seats from PostgreSQL: %v", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var showID string
		var available int64
		if err := rows.Scan(&showID, &available); err != nil {
			log.Printf("Metrics: failed to scan available seats: %v", err)
			return
		}
		ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, float64(available), showID)
	}
	if err := rows.Err(); err != nil {
		log.Printf("Metrics: failed to read available seats: %v", err)
	}
}

var (
	ReservationsConfirmed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "reservations_confirmed_total",
		Help: "Total number of confirmed reservations",
	})
	ReservationsHeld = promauto.NewCounter(prometheus.CounterOpts{
		Name: "reservations_held_total",
		Help: "Total number of reservations initially placed on hold",
	})
	IdempotentReplays = promauto.NewCounter(prometheus.CounterOpts{
		Name: "reservations_idempotent_replays_total",
		Help: "Total number of successful idempotency replays",
	})
	ReservationsDeclined = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "reservations_declined_total",
		Help: "Total number of declined reservations",
	}, []string{"reason"})
	HTTPRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total HTTP requests handled by the API",
	}, []string{"method", "route", "status"})
	HTTPRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request duration in seconds",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "route"})
	HoldReaperFailures = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hold_reaper_failures_total",
		Help: "Total failed hold expiry worker runs",
	})
	HoldReaperLastSuccess = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hold_reaper_last_success_timestamp_seconds",
		Help: "Unix timestamp of the last successful hold expiry worker run",
	})
)
