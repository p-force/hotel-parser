package storage

import (
	"slices"
	"strings"
	"testing"
)

func TestInventoryCSVAppendOnlyColumns(t *testing.T) {
	legacy := strings.Split("group_id,schema,property_id,source,source_property_id,run_id,run_started_at,run_finished_at,run_status,job_id,job_status,check_in,check_out,los,adults,rooms,attempt_id,attempt_no,collected_at,attempt_started_at,attempt_finished_at,attempt_outcome,http_status,error_code,category_id,category_name,area_m2,stock_status,available,variant_id,rate_id,rate_name,price,currency,reported_quantity", ",")
	added := strings.Split("property_physical_total_rooms,property_public_observed_max,property_physical_source,property_public_source,category_physical_total_rooms,category_public_observed_max", ",")
	diagnostics := strings.Split("attempt_warning_codes,category_warning_codes,has_offers,confirmed_unavailable", ",")
	if len(runCSVColumns) != 45 || !slices.Equal(runCSVColumns[:35], legacy) || !slices.Equal(runCSVColumns[35:41], added) || !slices.Equal(runCSVColumns[41:], diagnostics) {
		t.Fatal(runCSVColumns)
	}
}
