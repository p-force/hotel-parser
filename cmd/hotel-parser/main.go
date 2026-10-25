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
		loc = time.Local
	}

	slog.SetDefault(logger.New(os.Stdout, loc))
	slog.Info("hotel-parser started")
}
