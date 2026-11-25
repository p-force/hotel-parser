package schedule

import (
	"reflect"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/config"
)

func testRegistry(t *testing.T) Registry {
	t.Helper()
	hotel, err := config.Load("../../configs/arena33.json")
	if err != nil {
		t.Fatal(err)
	}
	hotel.Collection.Nights = []int{1} // Explicit legacy fixture; production registry is tested separately.
	return Registry{Groups: []Group{{ID: "arena33", Schema: "hotel_parser_arena33", Properties: []Property{{Hotel: hotel}}}}}
}
func slot(hour int) time.Time {
	return time.Date(2026, 10, 31, hour, 0, 0, 0, time.FixedZone("MSK", 3*3600))
}

func TestSlotScenariosAndIdentity(t *testing.T) {
	r := testRegistry(t)
	before := r.Groups[0].Properties[0].Hotel.Collection
	total := 0
	ids := map[string]bool{}
	for _, tc := range []struct{ hour, count int }{{8, 61}, {12, 8}, {16, 8}, {20, 1}} {
		plans, err := BuildPlans(r, slot(tc.hour))
		if err != nil || len(plans) != 1 {
			t.Fatalf("plans: %+v %v", plans, err)
		}
		p := plans[0]
		total += len(p.Scenarios)
		if len(p.Scenarios) != tc.count || ids[p.Run.ID] {
			t.Fatalf("slot count/identity: %+v", p)
		}
		ids[p.Run.ID] = true
		for i, sc := range p.Scenarios {
			if sc.Adults != 1 || sc.Rooms != 1 || sc.LOS != 1 || len(sc.ChildrenAges) != 0 || sc.CheckIn.Day() != slot(tc.hour).AddDate(0, 0, i).Day() || !sc.CheckOut.Equal(sc.CheckIn.AddDate(0, 0, 1)) {
				t.Fatalf("wrong scenario: %+v", sc)
			}
		}
		replay, err := BuildPlans(r, slot(tc.hour).UTC())
		if err != nil || !reflect.DeepEqual(p, replay[0]) {
			t.Fatal("delivery timezone changed plan identity")
		}
	}
	if total != 78 {
		t.Fatalf("daily count=%d", total)
	}
	if !reflect.DeepEqual(before, r.Groups[0].Properties[0].Hotel.Collection) {
		t.Fatal("planner changed diagnostic config")
	}
}

func TestTimezoneAndRegularLOS1(t *testing.T) {
	r := testRegistry(t)
	p := &r.Groups[0].Properties[0]
	p.Hotel.Timezone = "America/Los_Angeles"
	p.LOS = 1
	plans, err := BuildPlans(r, slot(8))
	if err != nil {
		t.Fatal(err)
	}
	if len(plans[0].Scenarios) != 61 || plans[0].Scenarios[0].CheckIn.Format("2006-01-02") != "2026-10-30" {
		t.Fatalf("object timezone ignored: %+v", plans[0])
	}
	for _, sc := range plans[0].Scenarios {
		if sc.LOS != 1 || sc.Adults != 1 || !sc.CheckOut.Equal(sc.CheckIn.AddDate(0, 0, 1)) {
			t.Fatalf("LOS replacement: %+v", sc)
		}
	}
	p.Hotel.Collection.Nights = []int{1, 2, 3}
	for _, los := range []int{2, 3} {
		p.LOS = los
		p.LOS2Evidence = ""
		plans, err := BuildPlans(r, slot(8))
		if err != nil || len(plans[0].Scenarios) != 8 {
			t.Fatal(plans, err)
		}
		for _, sc := range plans[0].Scenarios {
			if sc.LOS != los || !sc.CheckOut.Equal(sc.CheckIn.AddDate(0, 0, los)) {
				t.Fatal(sc)
			}
		}
	}

}

func TestRegistryAndSlotValidation(t *testing.T) {
	r, err := LoadRegistry("../../configs/groups.json")
	if err != nil || len(r.Groups) != 1 || len(r.Groups[0].Properties) != 7 {
		t.Fatalf("registry: %+v %v", r, err)
	}
	for _, bad := range []time.Time{time.Time{}, slot(9), slot(8).Add(time.Minute), slot(8).Add(time.Nanosecond)} {
		if _, err := BuildPlans(r, bad); err == nil {
			t.Fatalf("invalid slot accepted: %v", bad)
		}
	}
	for _, schema := range []string{"hotel_parser", "public", "pg_catalog", "bad-name", "x\";DROP SCHEMA public"} {
		r := testRegistry(t)
		r.Groups[0].Schema = schema
		if r.Validate() == nil {
			t.Fatalf("unsafe schema accepted: %s", schema)
		}
	}
	r = testRegistry(t)
	second := r.Groups[0]
	second.ID = "other"
	second.Schema = "hotel_parser_other"
	r.Groups = append(r.Groups, second)
	if err := r.Validate(); err != nil {
		t.Fatalf("cross-group property ID should be allowed: %v", err)
	}
	r.Groups[1].Schema = r.Groups[0].Schema
	if r.Validate() == nil {
		t.Fatal("shared schema accepted")
	}
}

func TestTravelLineCompetitorRegistry(t *testing.T) {
	r, err := LoadRegistry("../../configs/groups.json")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"monarch": "17249", "bega": "61", "azimut_aerostar": "585", "sovietsky": "333", "petroff": "5787"}
	for _, g := range r.Groups {
		if g.ID != "arena33" {
			continue
		}
		for _, p := range g.Properties {
			if code, ok := want[p.Hotel.ID]; ok {
				if !p.Hotel.Collection.Enabled || p.Hotel.Source.Type != "travelline" || p.Hotel.Source.PropertyID != code || p.LOS != 0 || p.LOS2Evidence != "" {
					t.Fatal(p)
				}
				delete(want, p.Hotel.ID)
			}
		}
	}
	if len(want) != 0 {
		t.Fatal("missing competitors", want)
	}
	plans, err := BuildPlans(r, slot(20))
	if err != nil || len(plans) != 6 {
		t.Fatal(plans, err)
	}
	for _, plan := range plans {
		if len(plan.Scenarios) != 3 {
			t.Fatal(plan)
		}
		s := plan.Scenarios[0]
		if s.LOS != 1 || s.Adults != 1 || s.Rooms != 1 {
			t.Fatal(s)
		}
	}
}

func TestFullTravelLineDiagnosticPlan(t *testing.T) {
	r, err := LoadRegistry("../../docs/verification/travelline-full-collection/registry.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Groups) != 1 || len(r.Groups[0].Properties) != 5 {
		t.Fatal("diagnostic registry must contain only five hotels")
	}
	for i := range r.Groups[0].Properties {
		r.Groups[0].Properties[i].Hotel.Collection.Nights = []int{1}
	}
	for _, p := range r.Groups[0].Properties {
		if p.Hotel.Source.Type != "travelline" {
			t.Fatal("Bnovo in diagnostic registry")
		}
	}
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}
	slot := time.Date(2026, 11, 3, 8, 0, 0, 0, loc)
	plans, err := BuildPlans(r, slot)
	if err != nil || len(plans) != 5 {
		t.Fatal(len(plans), err)
	}
	repeat, err := BuildPlans(r, slot)
	if err != nil || !reflect.DeepEqual(plans, repeat) {
		t.Fatal("unstable diagnostic resume plan", err)
	}
	jobs := 0
	for _, p := range plans {
		if len(p.Scenarios) != 61 || p.Run.PeriodStart.Format("2006-01-02") != "2026-11-03" || p.Run.PeriodEnd.Format("2006-01-02") != "2027-01-03" {
			t.Fatal("incorrect horizon", p.Run, len(p.Scenarios))
		}
		dates := map[string]bool{}
		for _, s := range p.Scenarios {
			date := s.CheckIn.Format("2006-01-02")
			if dates[date] || s.Adults != 1 || s.Rooms != 1 || s.LOS != 1 || len(s.ChildrenAges) != 0 || s.Language != "ru" {
				t.Fatal("incorrect scenario", s)
			}
			dates[date] = true
			if s.CheckOut.Format("2006-01-02") != s.CheckIn.AddDate(0, 0, 1).Format("2006-01-02") {
				t.Fatal("incorrect checkout", s)
			}
		}
		if !dates["2026-11-03"] || !dates["2027-01-02"] {
			t.Fatal("horizon boundary missing")
		}
		jobs += len(p.Scenarios)
	}
	if jobs != 305 {
		t.Fatal(jobs)
	}
}

func TestProductionRegistryDaily768Jobs(t *testing.T) {
	r, err := LoadRegistry("../../configs/groups.json")
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	ids := map[string]bool{}
	total := 0
	for _, hour := range []int{8, 12, 16, 20} {
		plans, err := BuildPlans(r, slot(hour))
		if err != nil || len(plans) != 6 {
			t.Fatal(plans, err)
		}
		replay, err := BuildPlans(r, slot(hour))
		if err != nil || !reflect.DeepEqual(plans, replay) {
			t.Fatal("unstable plan", err)
		}
		slotCount := 0
		for _, p := range plans {
			slotCount += len(p.Scenarios)
			if ids[p.Run.ID] {
				t.Fatal("duplicate run", p.Run.ID)
			}
			ids[p.Run.ID] = true
			counts[p.Run.PropertyID] += len(p.Scenarios)
			total += len(p.Scenarios)
		}
		if slotCount != map[int]int{8: 462, 12: 144, 16: 144, 20: 18}[hour] {
			t.Fatal(hour, slotCount)
		}
	}
	if total != 768 || len(counts) != 6 {
		t.Fatal(total, counts)
	}
	for id, n := range counts {
		if n != 128 || id == "lindsay" {
			t.Fatal(id, n)
		}
	}
}
