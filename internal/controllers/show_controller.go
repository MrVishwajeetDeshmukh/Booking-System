package controllers

import (
	"bookingsystem/internal/models"
	"bookingsystem/internal/services"
	"github.com/gofiber/fiber/v2"
	"strings"
)

type ShowController struct {
	svc *services.ShowService
}

func NewShowController(svc *services.ShowService) *ShowController {
	return &ShowController{svc: svc}
}

func (c *ShowController) HandleLiveness(ctx *fiber.Ctx) error {
	return ctx.SendString("OK")
}

func (c *ShowController) HandleReadiness(ctx *fiber.Ctx) error {
	if err := c.svc.Ping(ctx.Context()); err != nil {
		return ctx.Status(fiber.StatusServiceUnavailable).SendString("Database Unavailable")
	}
	return ctx.SendString("OK")
}

func (c *ShowController) HandleCreateShow(ctx *fiber.Ctx) error {
	var req models.ShowRequest
	if err := ctx.BodyParser(&req); err != nil {
		return ctx.Status(fiber.StatusBadRequest).SendString(err.Error())
	}
	if strings.TrimSpace(req.Name) == "" || len(req.Seats) == 0 || req.PricePaise < 0 {
		return ctx.Status(fiber.StatusBadRequest).SendString("Name, at least one seat, and a non-negative price_paise are required")
	}

	showID, err := c.svc.CreateShow(ctx.Context(), req)
	if err != nil {
		return ctx.Status(fiber.StatusInternalServerError).SendString("DB error")
	}

	return ctx.Status(fiber.StatusCreated).JSON(fiber.Map{
		"id":          showID,
		"name":        req.Name,
		"total_seats": len(req.Seats),
	})
}

func (c *ShowController) HandleGetShow(ctx *fiber.Ctx) error {
	showID := ctx.Params("id")
	counts, seats, err := c.svc.GetShow(ctx.Context(), showID)
	if err != nil {
		return ctx.Status(fiber.StatusInternalServerError).SendString("DB error")
	}

	return ctx.JSON(fiber.Map{
		"show_id": showID,
		"counts":  counts,
		"seats":   seats,
	})
}

func (c *ShowController) HandleReserve(ctx *fiber.Ctx) error {
	showID := ctx.Params("id")
	userID := ctx.Get("X-User-ID")
	if userID == "" {
		return ctx.Status(fiber.StatusUnauthorized).SendString("Missing X-User-ID")
	}

	idempotencyKey := ctx.Get("Idempotency-Key")
	if idempotencyKey == "" {
		return ctx.Status(fiber.StatusBadRequest).SendString("Missing Idempotency-Key")
	}

	var req models.ReserveRequest
	if err := ctx.BodyParser(&req); err != nil {
		return ctx.Status(fiber.StatusBadRequest).SendString(err.Error())
	}

	if len(req.Seats) == 0 {
		return ctx.Status(fiber.StatusBadRequest).SendString("No seats requested")
	}

	reqBodyBytes := ctx.Body()

	res, err := c.svc.ReserveSeats(ctx.Context(), showID, userID, idempotencyKey, req, reqBodyBytes)
	if err != nil {
		switch err.Error() {
		case "idempotent_mismatch":
			return ctx.Status(fiber.StatusConflict).SendString("Idempotency key reused with different body")
		case "limit_exceeded":
			return ctx.Status(fiber.StatusConflict).SendString("Per-user limit exceeded")
		case "seat_taken":
			return ctx.Status(fiber.StatusConflict).SendString("Seat already taken or invalid")
		default:
			return ctx.Status(fiber.StatusInternalServerError).SendString("DB error")
		}
	}

	return ctx.Status(fiber.StatusCreated).JSON(fiber.Map{
		"reservation_id": res.ReservationID,
		"show_id":        showID,
		"user_id":        userID,
		"seats":          res.Seats,
		"amount_paise":   res.AmountPaise,
		"status":         res.Status,
	})
}

func (c *ShowController) HandleConfirm(ctx *fiber.Ctx) error {
	resID := ctx.Params("id")
	userID := ctx.Get("X-User-ID")
	if userID == "" {
		return ctx.Status(fiber.StatusUnauthorized).SendString("Missing X-User-ID")
	}

	err := c.svc.ConfirmReservation(ctx.Context(), resID, userID)
	if err != nil {
		if err.Error() == "not_found_or_expired" {
			return ctx.Status(fiber.StatusNotFound).SendString("Hold not found or expired")
		}
		return ctx.Status(fiber.StatusInternalServerError).SendString("DB error")
	}

	return ctx.JSON(fiber.Map{
		"status": "confirmed",
	})
}

func (c *ShowController) HandleCancel(ctx *fiber.Ctx) error {
	resID := ctx.Params("id")
	userID := ctx.Get("X-User-ID")
	if userID == "" {
		return ctx.Status(fiber.StatusUnauthorized).SendString("Missing X-User-ID")
	}

	err := c.svc.CancelReservation(ctx.Context(), resID, userID)
	if err != nil {
		if err.Error() == "not_found" {
			return ctx.Status(fiber.StatusNotFound).SendString("Reservation not found or unauthorized")
		}
		return ctx.Status(fiber.StatusInternalServerError).SendString("DB error")
	}

	return ctx.JSON(fiber.Map{
		"status": "cancelled",
	})
}
