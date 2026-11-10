// Command inspect exports only explicit local diagnostic schemas in a read-only transaction.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/jackc/pgx/v5"
	"net"
	"os"
	"regexp"
	"time"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, "local readback failed")
		os.Exit(1)
	}
}
func run() error {
	schema := flag.String("schema", "", "isolated diagnostic schema")
	output := flag.String("out", "", "private readback JSON")
	flag.Parse()
	if !regexp.MustCompile(`^hp_(audit_20261110_[a-f0-9]{10}|regular_20261105_92f01e86)$`).MatchString(*schema) || *output == "" {
		return errors.New("refuse schema")
	}
	cfg, e := pgx.ParseConfig(os.Getenv("HOTEL_PARSER_DATABASE_URL"))
	if e != nil {
		return e
	}
	hosts := []string{cfg.Host}
	for _, f := range cfg.Fallbacks {
		hosts = append(hosts, f.Host)
	}
	for _, host := range hosts {
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return errors.New("refuse remote")
		}
	}
	cfg.RuntimeParams["application_name"] = "full_audit_readback"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c, e := pgx.ConnectConfig(ctx, cfg)
	if e != nil {
		return e
	}
	defer c.Close(ctx)
	tx, e := c.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead})
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	s := pgx.Identifier{*schema}.Sanitize()
	queries := map[string]string{
		"runs":         `SELECT id::text,property_id,period_start,period_end,started_at,finished_at,status FROM ` + s + `.collection_runs ORDER BY property_id`,
		"jobs":         `SELECT id::text,run_id::text,property_id,source,source_origin,source_property_id,check_in,check_out,los,adults,rooms,language,status,attempts,created_at,started_at,finished_at,next_retry_at,leased_until,last_error_code FROM ` + s + `.scenario_jobs ORDER BY property_id,check_in,adults,los`,
		"attempts":     `SELECT id::text,job_id::text,attempt_no,started_at,finished_at,duration_ms,queue_wait_ms,collected_at,outcome,http_status,error_code,retryable,warnings,next_retry_at FROM ` + s + `.collection_attempts ORDER BY job_id,attempt_no`,
		"observations": `SELECT id::text,attempt_id::text,category_id,category_name,area_m2::text,available,stock_status,has_offers,confirmed_unavailable,warnings FROM ` + s + `.observations ORDER BY attempt_id,category_id`,
		"offers":       `SELECT observation_id::text,variant_id,rate_id,rate_name,price::text,currency,meal_plan,visible,reported_quantity,data_available,row_available,data_available_for_guests,select_disabled,selectable_quantities FROM ` + s + `.observation_offers ORDER BY observation_id,variant_id,rate_id`,
		"variants":     `SELECT observation_id::text,variant_id,adults FROM ` + s + `.observation_variants ORDER BY observation_id,variant_id`,
	}
	result := map[string]json.RawMessage{}
	for key, query := range queries {
		var value []byte
		if e = tx.QueryRow(ctx, `SELECT COALESCE(json_agg(x),'[]'::json) FROM (`+query+`) x`).Scan(&value); e != nil {
			return e
		}
		result[key] = value
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	b, e := json.Marshal(result)
	if e != nil {
		return e
	}
	if e = os.WriteFile(*output, append(b, '\n'), 0600); e != nil {
		return e
	}
	fmt.Println("read-only local readback saved", *schema)
	return nil
}
