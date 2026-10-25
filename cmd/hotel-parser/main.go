package main

import (
	"log/slog"
	"os"
	"time"

	"github.com/p-force/hotel-parser/internal/logger"
)

func main() {
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		loc = time.FixedZone("UTC+3", 3*60*60)
	}

	slog.SetDefault(logger.New(os.Stdout, loc))

	if err != nil {
		slog.Warn("failed to load Europe/Moscow timezone", "error", err)
	}
	slog.Info("hotel-parser started")
}
