package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

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
	SeatsAvailable = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "seats_available",
		Help: "Number of available seats",
	}, []string{"show_id"})
)
