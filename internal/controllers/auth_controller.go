package controllers

import (
	"time"

	"bookingsystem/internal/auth"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type AuthController struct {
	secret []byte
}

func NewAuthController(secret []byte) *AuthController {
	return &AuthController{secret: secret}
}

func (c *AuthController) HandleRegister(ctx *fiber.Ctx) error {
	userID := uuid.NewString()
	now := time.Now()
	token, expiresAt, err := auth.IssueToken(c.secret, userID, now)
	if err != nil {
		return ctx.Status(fiber.StatusInternalServerError).SendString("Unable to issue bearer token")
	}
	return ctx.Status(fiber.StatusCreated).JSON(fiber.Map{
		"user_id":    userID,
		"token_type": "Bearer",
		"token":      token,
		"expires_at": expiresAt.UTC().Format(time.RFC3339),
	})
}
