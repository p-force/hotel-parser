package main

import (
	"fmt"
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

	log, logErr := logger.FromEnv(os.Stderr, loc)
	if logErr != nil {
		fmt.Fprintln(os.Stderr, logErr)
		os.Exit(1)
	}
	slog.SetDefault(log)

	if err != nil {
		slog.Warn("failed to load Europe/Moscow timezone", "error_code", "timezone_unavailable")
	}
	slog.Info("hotel-parser started")
}
