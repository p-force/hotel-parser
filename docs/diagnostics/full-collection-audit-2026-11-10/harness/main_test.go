package main

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/config"
)

func TestRecordedConfiguredAndRegularUnion(t *testing.T) {
	// Test this audit's recorded inputs, without constraining future hotel configs.
	data, e := os.ReadFile("../results/run-manifest.json")
	if e != nil {
		t.Fatal(e)
	}
	var manifest struct{ Hotels []config.Config }
	if e := json.Unmarshal(data, &manifest); e != nil {
		t.Fatal(e)
	}
	day := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	jobs, http := 0, 0
	hotels := 0
	for _, h := range manifest.Hotels {
		if !h.Collection.Enabled {
			continue
		}
		hotels++
		ss := scenarios(h, day)
		jobs += len(ss)
		http += len(ss)
		if h.Source.Type == "travelline" {
			http += len(ss)
		}
		keys := map[string]bool{}
		combinations := map[string]int{}
		for _, s := range ss {
			key := fmt.Sprintf("%s/%s/%d", s.CheckIn, s.CheckOut, s.Adults)
			if keys[key] {
				t.Fatal("duplicate scenario")
			}
			keys[key] = true
			combinations[fmt.Sprintf("%d/%d", s.Adults, s.LOS)]++
			if s.Rooms != 1 || s.LOS < 1 || s.Adults < 1 || s.Language != "ru" || len(s.ChildrenAges) > 0 || s.CheckOut.Sub(s.CheckIn) != time.Duration(s.LOS)*24*time.Hour || s.CheckIn.Before(day) || s.CheckIn.After(day.AddDate(0, 0, 60)) {
				t.Fatal(s)
			}
		}
		want := []string{"1/1"}
		if h.ID == "arena33" {
			want = append(want, "2/1", "2/2", "2/3")
		}
		if h.ID == "azimut_aerostar" {
			want = append(want, "2/1")
		}
		if len(combinations) != len(want) {
			t.Fatal(h.ID, combinations)
		}
		for _, key := range want {
			if combinations[key] != 61 {
				t.Fatal(h.ID, key, combinations)
			}
		}
	}
	if hotels != 6 || jobs != 610 || http != 976 {
		t.Fatal(hotels, jobs, http)
	}
}
func TestQuantileNearestRank(t *testing.T) {
	v := []int64{4, 1, 3, 2}
	if quantile(v, .5) != 2 || quantile(v, .95) != 4 || quantile(nil, .5) != 0 || v[0] != 4 {
		t.Fatal("quantile changed data or interpolated")
	}
}
func TestRefusesProductionDatabase(t *testing.T) {
	if _, e := localConfig("postgres://user@ep-example.neon.tech/neondb?sslmode=require"); e == nil {
		t.Fatal("remote allowed")
	}
	if _, e := localConfig("postgres://user@127.0.0.1/local?sslmode=disable"); e != nil {
		t.Fatal(e)
	}
}

func TestTaggingChangesEffectiveWorkerDSN(t *testing.T) {
	dsn, e := taggedDSN("postgres://reader@127.0.0.1/local?sslmode=disable", "hp_audit_test")
	if e != nil {
		t.Fatal(e)
	}
	cfg, e := localConfig(dsn)
	if e != nil || cfg.RuntimeParams["application_name"] != "hp_audit_test" || cfg.Host != "127.0.0.1" || cfg.Database != "local" {
		t.Fatal("effective connection tag missing")
	}
}
