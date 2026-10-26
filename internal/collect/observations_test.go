package collect

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/p-force/hotel-parser/internal/source/bnovo"
)

func TestAggregateCategoryStock(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*bnovo.SearchResult)
		status StockStatus
		count  *int
		groups int
	}{
		{"equal_counts", func(*bnovo.SearchResult) {}, StockKnown, stockInt(2), 1},
		{"different_counts", func(r *bnovo.SearchResult) {
			r.Offers[0].Availability = bnovo.AvailabilityEvidence{DataAvailable: stockInt(3), SelectableQuantities: []int{1, 2, 3}}
		}, StockConflict, nil, 1},
		{"unknown_is_not_zero", func(r *bnovo.SearchResult) { r.Offers = nil }, StockKnown, stockInt(2), 1},
		{"conflict_is_preserved", func(r *bnovo.SearchResult) { r.Offers[0].Availability.SelectableQuantities = []int{3} }, StockConflict, nil, 1},
		{"different_los", func(r *bnovo.SearchResult) { r.Request.CheckOut = r.Request.CheckOut.AddDate(0, 0, 1) }, StockKnown, stockInt(2), 2},
		{"different_hotel", func(r *bnovo.SearchResult) { r.Request.PropertyID = "other" }, StockKnown, stockInt(2), 2},
		{"different_dates", func(r *bnovo.SearchResult) {
			r.Request.CheckIn = r.Request.CheckIn.AddDate(0, 0, 1)
			r.Request.CheckOut = r.Request.CheckOut.AddDate(0, 0, 1)
		}, StockKnown, stockInt(2), 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first, second := stockFixture(), stockFixture()
			second.Request.Adults = 1
			second.Variants[0].Adults = 1
			tc.change(&second)
			before := stockSnapshot(t, second)
			groups := AggregateCategoryStock([]bnovo.SearchResult{first, second})
			if len(groups) != tc.groups || groups[0].Status != tc.status || !reflect.DeepEqual(groups[0].Available, tc.count) {
				t.Fatalf("groups=%+v", groups)
			}
			if !bytes.Equal(before, stockSnapshot(t, second)) {
				t.Fatal("input changed")
			}
			if tc.groups == 1 {
				obs := groups[0].Observations
				if len(obs) != 2 || !reflect.DeepEqual(obs[0].Request, first.Request) || !reflect.DeepEqual(obs[1].Request, second.Request) {
					t.Fatalf("scenarios lost: %+v", obs)
				}
				reversed := AggregateCategoryStock([]bnovo.SearchResult{second, first})
				if reversed[0].Status != groups[0].Status || !reflect.DeepEqual(reversed[0].Available, groups[0].Available) {
					t.Fatal("result depends on order")
				}
			}
		})
	}
	if got := AggregateCategoryStock(nil); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestAggregateConflictCannotRecover(t *testing.T) {
	a, b := stockFixture(), stockFixture()
	b.Offers[0].Availability = bnovo.AvailabilityEvidence{DataAvailable: stockInt(3), SelectableQuantities: []int{3}}
	for _, results := range [][]bnovo.SearchResult{{a, b, a}, {b, a, a}, {a, a, b}} {
		group := AggregateCategoryStock(results)[0]
		if group.Status != StockConflict || group.Available != nil || len(group.Observations) != 3 {
			t.Fatal(group)
		}
	}
}
