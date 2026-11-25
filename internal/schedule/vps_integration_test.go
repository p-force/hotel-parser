//go:build integration

package schedule

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/collect"
	"github.com/p-force/hotel-parser/internal/storage/storagetest"
)

func TestPostgresProductionRegistryDaily768Jobs(t *testing.T) {
	registry, err := LoadRegistry("../../configs/groups.json")
	if err != nil {
		t.Fatal(err)
	}
	for i := range registry.Groups {
		_, _, schema := storagetest.Open(t)
		registry.Groups[i].Schema = schema
	}
	s := testService(t, registry, nil, collect.RuntimeOptions{})
	for _, hour := range []int{8, 12, 16, 20} {
		for range 2 {
			if _, err := s.Execute(context.Background(), Request{Action: "plan", Slot: slot(hour)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	total := 0
	perHotel := map[string]int{}
	perSlot := map[int]int{}
	for _, g := range s.groups {
		progress, err := g.Store.RunProgress(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range progress {
			perSlot[p.Run.StartedAt.In(slot(8).Location()).Hour()] += p.Queued
			perHotel[p.Run.PropertyID] += p.Queued
			total += p.Queued
			if p.Succeeded != 0 || p.Run.FinishedAt != nil {
				t.Fatal(p)
			}
		}
	}
	for hour, want := range map[int]int{8: 462, 12: 144, 16: 144, 20: 18} {
		if perSlot[hour] != want {
			t.Fatal(hour, perSlot)
		}
	}
	if total != 768 || len(perHotel) != 6 {
		t.Fatal(total, perHotel)
	}
	for id, n := range perHotel {
		if n != 128 {
			t.Fatal(id, n)
		}
	}
}

func TestPostgresUnavailableFailsWithoutSourceTraffic(t *testing.T) {
	// A closed local port models PostgreSQL downtime; no database/server is stopped.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	t.Setenv("YANDEX_DISK_TOKEN", "")
	t.Setenv("HOTEL_PARSER_CSV_DIR", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s, err := Open(ctx, "postgres://unused:unused@"+address+"/unused?sslmode=disable", testRegistry(t), collect.RuntimeOptions{})
	if s != nil {
		s.Close()
	}
	if err == nil {
		t.Fatal("unavailable database accepted")
	}
	if strings.Contains(err.Error(), "unused") || strings.Contains(err.Error(), "postgres://") {
		t.Fatal("unsafe database diagnostic")
	}
}
