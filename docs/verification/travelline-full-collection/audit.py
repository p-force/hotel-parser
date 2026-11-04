#!/usr/bin/env python3
"""Offline audit of preserved source projections and committed CSV; no HTTP/DB."""
import collections
import csv
import datetime
import decimal
import hashlib
import json
import pathlib
import urllib.parse

ROOT = pathlib.Path(__file__).resolve().parent
summary = json.loads((ROOT / "summary.json").read_text())
source = {}
with (ROOT / "raw-quotes.csv").open() as stream:
    for row in csv.DictReader(stream):
        key = tuple(row[k] for k in ("hotel", "date", "category_id", "variant_id", "rate_id"))
        assert key not in source, ("duplicate source key", key)
        source[key] = (decimal.Decimal(row["price"]), row["currency"], row["reported_quantity"])
found = {}
scenarios = set()
rows_count = 0
unknown_rows = 0
for hotel in summary["results"]:
    body = (ROOT / (hotel["property"] + ".csv")).read_bytes()
    assert hashlib.sha256(body).hexdigest() == hotel["csv_sha256"]
    assert len(body) == hotel["csv_bytes"]
    counts = collections.Counter()
    with (ROOT / (hotel["property"] + ".csv")).open() as stream:
        reader = csv.DictReader(stream)
        assert len(reader.fieldnames) == 35
        rows = list(reader)
    assert len(rows) == hotel["csv_data_rows"]
    for row in rows:
        assert (row["source"], row["adults"], row["rooms"], row["los"]) == ("travelline", "1", "1", "1")
        assert row["run_id"] == hotel["run_id"]
        arrival = datetime.date.fromisoformat(row["check_in"])
        assert datetime.date.fromisoformat(row["check_out"]) == arrival + datetime.timedelta(days=1)
        scenarios.add((row["source_property_id"], row["check_in"]))
        counts[(row["job_id"], row["category_id"])] += 1
        if row["rate_id"]:
            key = tuple(row[k] for k in ("source_property_id", "check_in", "category_id", "variant_id", "rate_id"))
            assert key not in found, ("duplicate CSV offer", key)
            found[key] = (decimal.Decimal(row["price"]), row["currency"], row["reported_quantity"])
        else:
            assert row["stock_status"] == "unknown" and row["available"] == ""
            unknown_rows += 1
        rows_count += 1
    assert len(counts) == sum(hotel["stock_observations"].values())
assert found == source, "source quote/CSV price, currency or quota mismatch"
expected = {(h["hotel"], (datetime.date(2026, 11, 3) + datetime.timedelta(days=i)).isoformat())
            for h in summary["results"] for i in range(61)}
assert scenarios == expected and len(scenarios) == 305
assert len(source) == summary["counts"]["observation_offers"] == 7972
assert rows_count == 8280 and unknown_rows == 308
assert summary["metrics"]["actual_http_requests"] == 610
assert summary["metrics"]["actual_peak_http"] <= 2
assert summary["metrics"]["minimum_start_interval_s"] >= 1
assert summary["metrics"]["replay_http_requests"] == 0
with (ROOT / "http-starts.csv").open() as stream:
    events = list(csv.DictReader(stream))
assert len(events) == 610 and len({r["seq"] for r in events}) == 610
assert all(r["http_status"] == "200" and not r["safe_error"] for r in events)
starts = sorted(datetime.datetime.fromisoformat(r["started_at"].replace("Z", "+00:00")) for r in events)
assert min((b - a).total_seconds() for a, b in zip(starts, starts[1:])) >= 1
assert max(int(r["active_at_start"]) for r in events) <= 2
availability = set()
for event in events:
    url = urllib.parse.urlsplit(event["url"])
    assert url.scheme == "https" and url.hostname == "ru-ibe.tlintegration.ru"
    if url.path.endswith("hotel_availability"):
        query = urllib.parse.parse_qs(url.query)
        assert query["criterions[0].adults"] == ["1"]
        arrival, departure = query["criterions[0].dates"][0].split(";")
        assert datetime.date.fromisoformat(departure) == datetime.date.fromisoformat(arrival) + datetime.timedelta(days=1)
        availability.add((query["criterions[0].hotels[0].code"][0], arrival))
assert availability == expected
with (ROOT / "category-source-signals.csv").open() as stream:
    signals = list(csv.DictReader(stream))
assert len(signals) == 2379
assert sum(int(r["raw_stays"]) for r in signals) == 10773
assert sum(int(r["policy_excluded_stays"]) for r in signals) == 2801
unknown = {(r["hotel"], r["date"], r["category_id"]) for r in signals if r["status"] == "unknown"}
assert len(unknown) == 308
assert all(int(r["raw_stays"]) == 0 for r in signals if r["status"] == "unknown")
groups = json.loads((ROOT / "problem-groups.json").read_text())
assert len(groups) == 50 and sum(len(g["dates"]) for g in groups) == 308
assert {(g["hotel"], d, g["category_id"]) for g in groups for d in g["dates"]} == unknown
for item in json.loads((ROOT / "evidence/index.json").read_text()):
    body = (ROOT / item["projection_file"]).read_bytes()
    assert hashlib.sha256(body).hexdigest() == item["projection_sha256"]
print("PASS: 305 scenarios; 7972 source quotes; 8280 CSV rows; 308 unknown/NULL rows; checksums and limits")
