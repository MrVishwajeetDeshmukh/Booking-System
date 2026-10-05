package auth

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
)

func RequireUser(secret []byte) fiber.Handler {
	return func(ctx *fiber.Ctx) error {
		scheme, token, found := strings.Cut(strings.TrimSpace(ctx.Get(fiber.HeaderAuthorization)), " ")
		if !found || !strings.EqualFold(scheme, "Bearer") {
			return ctx.Status(fiber.StatusUnauthorized).SendString("Missing bearer token")
		}
		userID, err := VerifyToken(secret, strings.TrimSpace(token), time.Now())
		if err != nil {
			return ctx.Status(fiber.StatusUnauthorized).SendString("Invalid or expired bearer token")
		}
		ctx.Locals(LocalUserIDKey, userID)
		return ctx.Next()
	}
}
