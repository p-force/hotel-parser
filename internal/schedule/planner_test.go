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

func TestTimezoneAndConfirmedLOSReplacement(t *testing.T) {
	r := testRegistry(t)
	p := &r.Groups[0].Properties[0]
	p.Hotel.Timezone = "America/Los_Angeles"
	p.LOS = 2
	p.LOS2Evidence = "Recorded comparison: LOS=1 incomplete, LOS=2 returns all categories"
	plans, err := BuildPlans(r, slot(8))
	if err != nil {
		t.Fatal(err)
	}
	if len(plans[0].Scenarios) != 61 || plans[0].Scenarios[0].CheckIn.Format("2006-01-02") != "2026-10-30" {
		t.Fatalf("object timezone ignored: %+v", plans[0])
	}
	for _, sc := range plans[0].Scenarios {
		if sc.LOS != 2 || sc.Adults != 1 || !sc.CheckOut.Equal(sc.CheckIn.AddDate(0, 0, 2)) {
			t.Fatalf("LOS replacement: %+v", sc)
		}
	}
	p.LOS2Evidence = ""
	if _, err := BuildPlans(r, slot(8)); err == nil {
		t.Fatal("unconfirmed LOS=2 accepted")
	}
	p.LOS = 3
	if _, err := BuildPlans(r, slot(8)); err == nil {
		t.Fatal("scheduled LOS=3 accepted")
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
		if len(plan.Scenarios) != 1 {
			t.Fatal(plan)
		}
		s := plan.Scenarios[0]
		if s.LOS != 1 || s.Adults != 1 || s.Rooms != 1 {
			t.Fatal(s)
		}
	}
}
