package schedule

import (
	"testing"
	"time"
)

func TestDueSlotsMoscow(t *testing.T) {
	for _, tc := range []struct {
		now   string
		hours []int
	}{
		{"2026-11-05T04:59:59Z", nil},
		{"2026-11-05T05:00:00Z", []int{8}},
		{"2026-11-05T10:30:00Z", []int{8, 12}},
		{"2026-11-05T13:00:00Z", []int{8, 12, 16}},
		{"2026-11-05T20:59:59Z", []int{8, 12, 16, 20}},
		{"2026-11-05T21:00:00Z", nil},
	} {
		t.Run(tc.now, func(t *testing.T) {
			now, _ := time.Parse(time.RFC3339, tc.now)
			slots, err := DueSlots(now)
			if err != nil || len(slots) != len(tc.hours) {
				t.Fatal(slots, err)
			}
			for i, s := range slots {
				if s.Hour() != tc.hours[i] || s.After(now) || s.Minute() != 0 {
					t.Fatal(s)
				}
				if _, err := SlotHorizon(s); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	if _, err := DueSlots(time.Time{}); err == nil {
		t.Fatal("zero time accepted")
	}
}

func TestActiveRegistryRegularLOS1(t *testing.T) {
	r, err := LoadRegistry("../../configs/groups.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, hour := range []int{8, 12, 16, 20} {
		plans, err := BuildPlans(r, slot(hour))
		if err != nil || len(plans) != 6 {
			t.Fatal(len(plans), err)
		}
		for _, p := range plans {
			if p.Run.PropertyID == "lindsay" {
				t.Fatal("disabled hotel planned")
			}
			for _, sc := range p.Scenarios {
				if sc.LOS != 1 || sc.Adults != 1 || sc.Rooms != 1 || len(sc.ChildrenAges) > 0 || sc.Language != "ru" {
					t.Fatal(sc)
				}
			}
		}
	}
}
