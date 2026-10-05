package routes

import (
	"context"

	"bookingsystem/internal/controllers"
	"bookingsystem/internal/services"
	"github.com/gofiber/adaptor/v2"
	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func SetupRoutes(app *fiber.App, db *pgxpool.Pool) {
	svc := services.NewShowService(db)

	// Start background goroutine to expire holds after 15 minutes
	svc.StartHoldReaper(context.Background())

	ctrl := controllers.NewShowController(svc)

	app.Get("/healthz", ctrl.HandleLiveness)
	app.Get("/readyz", ctrl.HandleReadiness)
	app.Get("/metrics", adaptor.HTTPHandler(promhttp.Handler()))

	app.Post("/shows", ctrl.HandleCreateShow)
	app.Get("/shows/:id", ctrl.HandleGetShow)
	app.Post("/shows/:id/reserve", ctrl.HandleReserve)
	app.Post("/reservations/:id/confirm", ctrl.HandleConfirm)
	app.Post("/reservations/:id/cancel", ctrl.HandleCancel)
}
