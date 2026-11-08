package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/p-force/hotel-parser/internal/collect"
	"github.com/p-force/hotel-parser/internal/logger"
	"github.com/p-force/hotel-parser/internal/schedule"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: collection migrate|plan|resume|tick|serve [flags]")
	}
	action := args[0]
	flags := flag.NewFlagSet("collection", flag.ContinueOnError)
	registryPath := flags.String("groups", "configs/groups.json", "group registry")
	slotText := flags.String("slot", "", "logical slot, RFC3339 with timezone")
	execution := flags.Duration("execution-timeout", 45*time.Second, "execution budget")
	grace := flags.Duration("shutdown-grace-period", 5*time.Second, "total flush budget")
	maxJobs := flags.Int("max-jobs", 64, "maximum claimed jobs per invocation")
	workers := flags.Int("workers", 8, "shared worker count")
	listen := flags.String("listen", "127.0.0.1:8080", "HTTP listen address")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	var slot time.Time
	var err error
	if *slotText != "" {
		slot, err = time.Parse(time.RFC3339, *slotText)
		if err != nil {
			return fmt.Errorf("invalid slot timestamp")
		}
	}
	if action != "serve" && action != "migrate" {
		if err := schedule.ValidateRequest(schedule.Request{Action: action, Slot: slot}); err != nil {
			return err
		}
	}
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		return err
	}
	log, err := logger.FromEnv(os.Stderr, loc)
	if err != nil {
		return err
	}
	slog.SetDefault(log)
	registry, err := schedule.LoadRegistry(*registryPath)
	if err != nil {
		return err
	}
	openCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	service, err := schedule.Open(openCtx, os.Getenv("HOTEL_PARSER_DATABASE_URL"), registry, collect.RuntimeOptions{Batch: collect.BatchOptions{Workers: *workers}, ExecutionTimeout: *execution, ShutdownGracePeriod: *grace, MaxJobs: *maxJobs})
	cancel()
	if err != nil {
		return err
	}
	defer service.Close()
	if action == "migrate" {
		migrateCtx, end := context.WithTimeout(ctx, *execution)
		defer end()
		return service.Migrate(migrateCtx)
	}
	if action == "serve" {
		handler, err := service.Handler(os.Getenv("HOTEL_PARSER_TRIGGER_SECRET"))
		if err != nil {
			return err
		}
		mux := http.NewServeMux()
		mux.Handle("/trigger", handler)
		server := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
		done := make(chan error, 1)
		go func() { done <- server.ListenAndServe() }()
		select {
		case err := <-done:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return fmt.Errorf("HTTP listener failed")
		case <-ctx.Done():
			shutdown, end := context.WithTimeout(context.Background(), *grace+time.Second)
			defer end()
			err := server.Shutdown(shutdown)
			if err != nil {
				_ = server.Close()
			}
			<-done
			return err
		}
	}
	response, err := service.Execute(ctx, schedule.Request{Action: action, Slot: slot})
	encodeErr := json.NewEncoder(os.Stdout).Encode(response)
	return errors.Join(err, encodeErr)
}
