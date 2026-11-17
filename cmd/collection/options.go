package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/p-force/hotel-parser/internal/collect"
	"github.com/p-force/hotel-parser/internal/source/travelline"
)

type commandOptions struct {
	collect.RuntimeOptions
	Limits collect.Limits
}

// Переменные задают defaults CLI; явные флаги имеют приоритет.
func optionsFromEnv() (commandOptions, error) {
	o := commandOptions{RuntimeOptions: collect.RuntimeOptions{Batch: collect.BatchOptions{Workers: 8}, MaxJobs: 64, ExecutionTimeout: 45 * time.Second, ShutdownGracePeriod: 5 * time.Second}}
	property, bnovo, tl := 4, 8, travelline.DefaultConcurrency
	for _, p := range []struct {
		key   string
		value *int
		max   int
	}{
		{"HOTEL_PARSER_WORKERS", &o.Batch.Workers, collect.MaxWorkers},
		{"HOTEL_PARSER_PROPERTY_CONCURRENCY", &property, collect.MaxWorkers},
		{"HOTEL_PARSER_BNOVO_CONCURRENCY", &bnovo, collect.MaxWorkers},
		{"HOTEL_PARSER_TRAVELLINE_CONCURRENCY", &tl, collect.MaxWorkers},
		{"HOTEL_PARSER_MAX_JOBS", &o.MaxJobs, int(^uint(0) >> 1)},
	} {
		if raw, ok := os.LookupEnv(p.key); ok {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > p.max {
				return o, fmt.Errorf("invalid %s", p.key)
			}
			*p.value = n
		}
	}
	bnInterval, tlInterval := 500*time.Millisecond, travelline.DefaultStartInterval
	for _, p := range []struct {
		key      string
		value    *time.Duration
		positive bool
	}{
		{"HOTEL_PARSER_BNOVO_START_INTERVAL", &bnInterval, false},
		{"HOTEL_PARSER_TRAVELLINE_START_INTERVAL", &tlInterval, false},
		{"HOTEL_PARSER_EXECUTION_TIMEOUT", &o.ExecutionTimeout, true},
		{"HOTEL_PARSER_SHUTDOWN_GRACE_PERIOD", &o.ShutdownGracePeriod, true},
	} {
		if raw, ok := os.LookupEnv(p.key); ok {
			d, err := time.ParseDuration(raw)
			if err != nil || d < 0 || (p.positive && d == 0) {
				return o, fmt.Errorf("invalid %s", p.key)
			}
			*p.value = d
		}
	}
	o.Limits = collect.Limits{PropertyConcurrency: property, Sources: map[string]collect.SourceLimit{
		"https://reservationsteps.ru": {Concurrency: bnovo, StartInterval: bnInterval},
		travelline.PublicOrigin:       {Concurrency: tl, StartInterval: tlInterval},
	}}
	return o, nil
}
