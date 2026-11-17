package bnovo

import (
	"testing"
)

func TestVariantMatchesAdults(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name             string
		variant, request int
		row              *bool
		want             bool
	}{
		{"legacy_exact", 2, 2, nil, true},
		{"legacy_larger_is_not_proof", 2, 1, nil, false},
		{"one_guest_in_two_guest_variant", 2, 1, &yes, true},
		{"one_guest_in_three_guest_variant", 3, 1, &yes, true},
		{"two_guests_in_four_guest_variant", 4, 2, &yes, true},
		{"insufficient_even_with_flag", 1, 2, &yes, false},
		{"row_not_confirmed", 2, 1, &no, false},
		{"exact_row_not_confirmed", 2, 2, &no, false},
		{"unknown_guest_count", 0, 1, &yes, false},
		{"invalid_request", 2, 0, &yes, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			variant := AccommodationVariant{Adults: tc.variant}
			if got := variant.MatchesAdults(tc.request, AvailabilityEvidence{RowAvailable: tc.row}); got != tc.want {
				t.Errorf("matches=%v want=%v", got, tc.want)
			}
		})
	}
}
