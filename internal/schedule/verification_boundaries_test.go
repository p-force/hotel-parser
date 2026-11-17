package schedule

import (
	"testing"
	"time"
)

func TestVerificationCalendarBoundaries(t *testing.T) {
	for _, tc := range []struct {
		slot, zone, first, last, checkout string
		count                             int
	}{
		{"2026-12-31T05:00:00Z", "Europe/Moscow", "2026-12-31", "2027-03-01", "2027-03-02", 61},
		{"2027-01-31T05:00:00Z", "Europe/Moscow", "2027-01-31", "2027-04-01", "2027-04-02", 61},
		{"2026-12-31T17:00:00Z", "Pacific/Auckland", "2027-01-01", "2027-01-01", "2027-01-02", 1},
	} {
		t.Run(tc.slot+"/"+tc.zone, func(t *testing.T) {
			r := testRegistry(t)
			r.Groups[0].Properties[0].Hotel.Timezone = tc.zone
			slot, err := time.Parse(time.RFC3339, tc.slot)
			if err != nil {
				t.Fatal(err)
			}
			plans, err := BuildPlans(r, slot)
			if err != nil {
				t.Fatal(err)
			}
			jobs := plans[0].Scenarios
			if len(jobs) != tc.count || jobs[0].CheckIn.Format("2006-01-02") != tc.first || jobs[len(jobs)-1].CheckIn.Format("2006-01-02") != tc.last || jobs[len(jobs)-1].CheckOut.Format("2006-01-02") != tc.checkout {
				t.Fatalf("calendar boundary incorrect: first=%v last=%v", jobs[0], jobs[len(jobs)-1])
			}
		})
	}
}
