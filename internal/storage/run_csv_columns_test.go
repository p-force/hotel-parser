package storage

import (
	"slices"
	"strings"
	"testing"
)

func TestAnalyticalCSVV3Columns(t *testing.T) {
	expected := strings.Split("group_id,property_id,source,run_id,run_started_at,job_status,check_in,check_out,los,adults,rooms,collected_at,http_status,error_code,category_id,category_name,area_m2,stock_status,available,variant_id,rate_id,rate_name,price,currency,reported_quantity,property_physical_total_rooms,property_public_observed_max,property_physical_source,property_public_source,category_physical_total_rooms,category_public_observed_max,attempt_warning_codes,category_warning_codes,has_offers,confirmed_unavailable", ",")
	if len(runCSVColumns) != 35 || !slices.Equal(runCSVColumns, expected) {
		t.Fatal(runCSVColumns)
	}
	for _, removed := range strings.Split("schema,source_property_id,run_finished_at,run_status,job_id,attempt_id,attempt_no,attempt_started_at,attempt_finished_at,attempt_outcome", ",") {
		if slices.Contains(runCSVColumns, removed) {
			t.Fatal("technical column leaked", removed)
		}
	}
}
