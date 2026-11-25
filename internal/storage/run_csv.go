package storage

import (
	"context"
	"encoding/csv"
	"io"
	"time"
)

// runCSVColumns — контракт CSV v2: первые 35 колонок сохранены из v1. Пустая ячейка обозначает SQL NULL.
var runCSVColumns = []string{
	"group_id", "schema", "property_id", "source", "source_property_id",
	"run_id", "run_started_at", "run_finished_at", "run_status", "job_id", "job_status",
	"check_in", "check_out", "los", "adults", "rooms", "attempt_id", "attempt_no",
	"collected_at", "attempt_started_at", "attempt_finished_at", "attempt_outcome", "http_status", "error_code",
	"category_id", "category_name", "area_m2", "stock_status", "available",
	"variant_id", "rate_id", "rate_name", "price", "currency", "reported_quantity",
	"property_physical_total_rooms", "property_public_observed_max", "property_physical_source", "property_public_source",
	"category_physical_total_rooms", "category_public_observed_max",
	"attempt_warning_codes", "category_warning_codes", "has_offers", "confirmed_unavailable",
}

// WriteRunCSV пишет только финальную сохранённую попытку каждого job одного
// завершённого run. Отсутствие observation/offer сохраняется строкой LEFT JOIN.
// PostgreSQL rows и csv.Writer обрабатываются потоково; память не зависит от
// количества jobs/offers. Метод не требует локального диска и не исполняет HTTP.
// При ошибке writer может содержать префикс CSV: публикация — обязанность вызывающего.
func (s *Store) WriteRunCSV(ctx context.Context, groupID, runID string, output io.Writer) (int64, error) {
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		return 0, err
	}
	if run.Status == RunRunning || run.FinishedAt == nil {
		return 0, ErrIncomplete
	}
	const stamp = `'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'`
	query := `SELECT j.source,j.source_property_id,j.id::text,j.status,
 to_char(j.check_in,'YYYY-MM-DD'),to_char(j.check_out,'YYYY-MM-DD'),j.los::text,j.adults::text,j.rooms::text,
 COALESCE(a.id::text,''),COALESCE(a.attempt_no::text,''),
 COALESCE(to_char(a.collected_at AT TIME ZONE 'UTC',` + stamp + `),''),
 COALESCE(to_char(a.started_at AT TIME ZONE 'UTC',` + stamp + `),''),
 COALESCE(to_char(a.finished_at AT TIME ZONE 'UTC',` + stamp + `),''),
 COALESCE(a.outcome,''),COALESCE(a.http_status::text,''),COALESCE(a.error_code,j.last_error_code,''),
 COALESCE(o.category_id,''),COALESCE(o.category_name,''),COALESCE(o.area_m2::text,''),
 COALESCE(o.stock_status,''),COALESCE(o.available::text,''),
 COALESCE(f.variant_id,''),COALESCE(f.rate_id,''),COALESCE(f.rate_name,''),
 COALESCE(f.price::text,''),COALESCE(f.currency,''),COALESCE(f.reported_quantity::text,''),
 COALESCE(ri.physical_total_rooms::text,''),COALESCE(ri.public_observed_max::text,''),
 COALESCE(ri.physical_source,''),COALESCE(ri.public_source,''),
 COALESCE(ci.physical_total_rooms::text,''),COALESCE(ci.public_observed_max::text,''),
 COALESCE(a.warnings::text,''),COALESCE(o.warnings::text,''),
 COALESCE(o.has_offers::text,''),COALESCE(o.confirmed_unavailable::text,'')
 FROM ` + s.table("scenario_jobs") + ` j
 LEFT JOIN ` + s.table("collection_attempts") + ` a ON a.job_id=j.id AND a.attempt_no=j.attempts
 LEFT JOIN ` + s.table("observations") + ` o ON o.attempt_id=a.id
 LEFT JOIN ` + s.table("observation_offers") + ` f ON f.observation_id=o.id
 LEFT JOIN ` + s.table("run_inventory") + ` ri ON ri.run_id=j.run_id
 LEFT JOIN ` + s.table("run_category_inventory") + ` ci ON ci.run_id=j.run_id AND ci.source_room_id=o.category_id
 WHERE j.run_id=$1::uuid
 ORDER BY j.check_in,j.check_out,j.adults,j.rooms,j.id,o.category_id,f.variant_id,f.rate_id`
	rows, err := s.db.QueryContext(ctx, query, run.ID)
	if err != nil {
		return 0, dbError("read run CSV", err)
	}
	defer rows.Close()
	writer := csv.NewWriter(output)
	if err := writer.Write(runCSVColumns); err != nil {
		return 0, err
	}
	prefix := []string{groupID, s.schema, run.PropertyID}
	suffix := []string{run.ID, run.StartedAt.UTC().Format(time.RFC3339Nano), run.FinishedAt.UTC().Format(time.RFC3339Nano), string(run.Status)}
	var count int64
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		fields := make([]string, len(runCSVColumns)-7)
		dest := make([]any, len(fields))
		for i := range fields {
			dest[i] = &fields[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return count, dbError("scan run CSV", err)
		}
		record := make([]string, 0, len(runCSVColumns))
		record = append(record, prefix...)
		record = append(record, fields[:2]...)
		record = append(record, suffix...)
		record = append(record, fields[2:]...)
		if err := writer.Write(record); err != nil {
			return count, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return count, dbError("iterate run CSV", err)
	}
	if err := ctx.Err(); err != nil {
		return count, err
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return count, err
	}
	return count, ctx.Err()
}
