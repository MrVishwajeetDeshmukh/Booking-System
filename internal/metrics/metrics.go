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
	ReservationsDeclined = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "reservations_declined_total",
		Help: "Total number of declined reservations",
	}, []string{"reason"})
	SeatsAvailable = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "seats_available",
		Help: "Number of available seats",
	}, []string{"show_id"})
)
