package schedule

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestMultiLOSSlotHorizonsAndPeriodEnd(t *testing.T) {
	r := testRegistry(t)
	r.Groups[0].Properties[0].Hotel.Collection.Nights = []int{1, 2, 3}
	for _, tc := range []struct {
		hour, total int
		perLOS      map[int]int
	}{
		{8, 77, map[int]int{1: 61, 2: 8, 3: 8}}, {12, 24, map[int]int{1: 8, 2: 8, 3: 8}},
		{16, 24, map[int]int{1: 8, 2: 8, 3: 8}}, {20, 3, map[int]int{1: 1, 2: 1, 3: 1}},
	} {
		t.Run(fmt.Sprint(tc.hour), func(t *testing.T) {
			plans, err := BuildPlans(r, slot(tc.hour))
			if err != nil {
				t.Fatal(err)
			}
			p := plans[0]
			if len(p.Scenarios) != tc.total {
				t.Fatal(len(p.Scenarios), tc.total)
			}
			counts := map[int]int{}
			seen := map[string]bool{}
			var maxOut time.Time
			for _, sc := range p.Scenarios {
				wantIn := p.Run.PeriodStart.AddDate(0, 0, counts[sc.LOS])
				counts[sc.LOS]++
				key := fmt.Sprintf("%s/%d", sc.CheckIn.Format("2006-01-02"), sc.LOS)
				if seen[key] || !sc.CheckIn.Equal(wantIn) || !sc.CheckOut.Equal(sc.CheckIn.AddDate(0, 0, sc.LOS)) || sc.Adults != 1 || sc.Rooms != 1 || len(sc.ChildrenAges) != 0 || sc.Language != "ru" {
					t.Fatal(sc, key)
				}
				seen[key] = true
				if sc.CheckOut.After(maxOut) {
					maxOut = sc.CheckOut
				}
			}
			if !reflect.DeepEqual(counts, tc.perLOS) || !p.Run.PeriodEnd.Equal(maxOut) {
				t.Fatal(counts, p.Run, maxOut)
			}
			r.Groups[0].Properties[0].Hotel.Collection.Nights = []int{3, 1, 2}
			replay, err := BuildPlans(r, slot(tc.hour).UTC())
			if err != nil || !reflect.DeepEqual(plans, replay) {
				t.Fatal("nights order changed identity", err)
			}
			r.Groups[0].Properties[0].Hotel.Collection.Nights = []int{1, 2, 3}
		})
	}
}

func TestMultiLOSSubsetsAndCalendarBoundaries(t *testing.T) {
	for _, nights := range [][]int{{1}, {2}, {3}, {2, 3}} {
		r := testRegistry(t)
		p := &r.Groups[0].Properties[0]
		p.Hotel.Collection.Nights = nights
		p.Hotel.Timezone = "America/Los_Angeles"
		at := time.Date(2027, 3, 13, 8, 0, 0, 0, time.FixedZone("MSK", 3*3600))
		plans, err := BuildPlans(r, at)
		if err != nil {
			t.Fatal(err)
		}
		plan := plans[0]
		var maxOut time.Time
		for _, sc := range plan.Scenarios {
			if !sc.CheckOut.Equal(sc.CheckIn.AddDate(0, 0, sc.LOS)) {
				t.Fatal("DST checkout", sc)
			}
			if sc.CheckOut.After(maxOut) {
				maxOut = sc.CheckOut
			}
		}
		if !maxOut.Equal(plan.Run.PeriodEnd) {
			t.Fatal(nights, plan.Run, maxOut)
		}
		expected := 8 * len(nights)
		for _, los := range nights {
			if los == 1 {
				expected += 53
			}
		}
		if len(plan.Scenarios) != expected {
			t.Fatal(nights, len(plan.Scenarios), expected)
		}
	}
}

func TestLegacyRegistryLOSOverrides(t *testing.T) {
	for _, tc := range []struct {
		los      int
		evidence string
		nights   []int
		valid    bool
	}{
		{0, "", []int{1, 2, 3}, true}, {1, "", []int{1, 2, 3}, true},
		{2, "", []int{1, 2, 3}, true}, {2, "old documented restriction", []int{1, 2, 3}, true},
		{3, "", []int{1, 2, 3}, true}, {2, "old evidence", []int{1}, false},
		{0, "unpaired", []int{1, 2, 3}, false}, {3, "unpaired", []int{1, 2, 3}, false}, {4, "", []int{1, 2, 3}, false},
	} {
		r := testRegistry(t)
		p := &r.Groups[0].Properties[0]
		p.LOS = tc.los
		p.LOS2Evidence = tc.evidence
		p.Hotel.Collection.Nights = tc.nights
		plans, err := BuildPlans(r, slot(20))
		if !tc.valid {
			if err == nil {
				t.Fatal("unsafe legacy override accepted", tc)
			}
			continue
		}
		if err != nil {
			t.Fatal(tc, err)
		}
		want := len(tc.nights)
		if tc.los != 0 {
			want = 1
		}
		if len(plans[0].Scenarios) != want {
			t.Fatal(tc, plans)
		}
		for _, sc := range plans[0].Scenarios {
			if tc.los != 0 && sc.LOS != tc.los {
				t.Fatal(tc, sc)
			}
		}
	}
}
