package main

import (
	"context"
	"encoding/json"
	_ "embed"
	"log"
	"os"
	"strings"
	"time"

	"bookingsystem/internal/routes"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/gofiber/fiber/v2/middleware/requestid"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed db/schema.sql
var schemaSQL string

func main() {
	tokenSecret := []byte(os.Getenv("AUTH_TOKEN_SECRET"))
	if len(tokenSecret) < 32 {
		log.Fatal("AUTH_TOKEN_SECRET must be set to at least 32 bytes")
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL must be set")
	}

	ctx := context.Background()
	db, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("Unable to connect to database: %v", err)
	}
	defer db.Close()
	if err := db.Ping(ctx); err != nil {
		log.Fatalf("Unable to reach database: %v", err)
	}
	for _, statement := range strings.Split(schemaSQL, ";") {
		if strings.TrimSpace(statement) == "" {
			continue
		}
		if _, err := db.Exec(ctx, statement); err != nil {
			log.Fatalf("Unable to initialize database schema: %v", err)
		}
	}

	app := fiber.New()
	app.Use(requestid.New())
	accessLog := log.New(os.Stdout, "", 0)
	app.Use(func(c *fiber.Ctx) error {
		started := time.Now()
		err := c.Next()
		status := c.Response().StatusCode()
		if err != nil {
			status = fiber.StatusInternalServerError
			if fiberErr, ok := err.(*fiber.Error); ok {
				status = fiberErr.Code
			}
		}
		level := "INFO"
		if status >= fiber.StatusInternalServerError {
			level = "ERROR"
		}
		entry := struct {
			Timestamp string `json:"timestamp"`
			Level     string `json:"level"`
			RequestID string `json:"request_id"`
			Status    int    `json:"status"`
			LatencyMS int64  `json:"latency_ms"`
			Method    string `json:"method"`
			Path      string `json:"path"`
			Error     string `json:"error,omitempty"`
		}{
			Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
			Level:     level,
			Status:    status,
			LatencyMS: time.Since(started).Milliseconds(),
			Method:    c.Method(),
			Path:      c.Path(),
		}
		if requestID, ok := c.Locals("requestid").(string); ok {
			entry.RequestID = requestID
		}
		if err != nil {
			entry.Error = err.Error()
		}
		if encoded, marshalErr := json.Marshal(entry); marshalErr == nil {
			accessLog.Print(string(encoded))
		}
		return err
	})
	app.Use(recover.New())

	if err := routes.SetupRoutes(app, db, tokenSecret); err != nil {
		log.Fatalf("Unable to configure routes: %v", err)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("Starting server on port %s", port)
	if err := app.Listen(":" + port); err != nil {
		log.Fatalf("Unable to serve HTTP: %v", err)
	}
}
