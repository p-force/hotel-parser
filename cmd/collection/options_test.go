package main

import (
	"strings"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/source/travelline"
)

func TestVPSOptionsFromEnv(t *testing.T) {
	for k, v := range map[string]string{
		"HOTEL_PARSER_WORKERS": "4", "HOTEL_PARSER_PROPERTY_CONCURRENCY": "2",
		"HOTEL_PARSER_TRAVELLINE_CONCURRENCY": "2", "HOTEL_PARSER_TRAVELLINE_START_INTERVAL": "1s",
		"HOTEL_PARSER_BNOVO_CONCURRENCY": "2", "HOTEL_PARSER_BNOVO_START_INTERVAL": "500ms",
		"HOTEL_PARSER_MAX_JOBS": "64", "HOTEL_PARSER_EXECUTION_TIMEOUT": "45s", "HOTEL_PARSER_SHUTDOWN_GRACE_PERIOD": "5s",
	} {
		t.Setenv(k, v)
	}
	o, err := optionsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if o.Batch.Workers != 4 || o.MaxJobs != 64 || o.ExecutionTimeout != 45*time.Second || o.ShutdownGracePeriod != 5*time.Second || o.Limits.PropertyConcurrency != 2 {
		t.Fatal(o)
	}
	for origin, interval := range map[string]time.Duration{travelline.PublicOrigin: time.Second, "https://reservationsteps.ru": 500 * time.Millisecond} {
		if l := o.Limits.Sources[origin]; l.Concurrency != 2 || l.StartInterval != interval {
			t.Fatal(origin, l)
		}
	}
}

func TestInvalidLimitsFailBeforeOpeningDatabase(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"HOTEL_PARSER_WORKERS", "0"}, {"HOTEL_PARSER_WORKERS", "9999"},
		{"HOTEL_PARSER_PROPERTY_CONCURRENCY", "-1"}, {"HOTEL_PARSER_BNOVO_CONCURRENCY", "bad"},
		{"HOTEL_PARSER_TRAVELLINE_CONCURRENCY", "0"}, {"HOTEL_PARSER_MAX_JOBS", "-1"},
		{"HOTEL_PARSER_BNOVO_START_INTERVAL", "-1s"}, {"HOTEL_PARSER_TRAVELLINE_START_INTERVAL", "bad"},
		{"HOTEL_PARSER_EXECUTION_TIMEOUT", "0s"}, {"HOTEL_PARSER_SHUTDOWN_GRACE_PERIOD", "bad"},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			if err := run(t.Context(), []string{"resume"}); err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatal(err)
			}
		})
	}
}
