package routes

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"bookingsystem/internal/auth"
	"bookingsystem/internal/controllers"
	"bookingsystem/internal/metrics"
	"bookingsystem/internal/services"
	"github.com/gofiber/adaptor/v2"
	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func SetupRoutes(app *fiber.App, db *pgxpool.Pool, tokenSecret []byte) error {
	if err := metrics.RegisterAvailableSeatsCollector(db); err != nil {
		return fmt.Errorf("register available-seat Prometheus collector: %w", err)
	}
	svc := services.NewShowService(db)

	// Start background goroutine to expire holds after 15 minutes
	svc.StartHoldReaper(context.Background())

	app.Use(func(c *fiber.Ctx) error {
		started := time.Now()
		err := c.Next()
		route := c.Route().Path
		if route == "" {
			route = "unmatched"
		}
		status := c.Response().StatusCode()
		if err != nil {
			status = fiber.StatusInternalServerError
			if fiberErr, ok := err.(*fiber.Error); ok {
				status = fiberErr.Code
			}
		}
		metrics.HTTPRequests.WithLabelValues(c.Method(), route, strconv.Itoa(status)).Inc()
		metrics.HTTPRequestDuration.WithLabelValues(c.Method(), route).Observe(time.Since(started).Seconds())
		return err
	})

	ctrl := controllers.NewShowController(svc)
	authCtrl := controllers.NewAuthController(tokenSecret)
	requireUser := auth.RequireUser(tokenSecret)

	app.Get("/healthz", ctrl.HandleLiveness)
	app.Get("/readyz", ctrl.HandleReadiness)
	app.Get("/metrics", adaptor.HTTPHandler(promhttp.Handler()))
	app.Post("/auth/register", authCtrl.HandleRegister)

	app.Post("/shows", ctrl.HandleCreateShow)
	app.Get("/shows/:id", ctrl.HandleGetShow)
	app.Post("/shows/:id/reserve", requireUser, ctrl.HandleReserve)
	app.Post("/reservations/:id/confirm", requireUser, ctrl.HandleConfirm)
	app.Post("/reservations/:id/cancel", requireUser, ctrl.HandleCancel)
	return nil
}
