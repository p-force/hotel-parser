#!/usr/bin/env python3
"""Offline analysis of a measured run and read-only PostgreSQL export. No network."""
import argparse
import collections
import csv
import datetime as dt
import decimal
import hashlib
import json
import math
import pathlib


def read(path):
    return json.loads(path.read_text())


def timestamp(value):
    return dt.datetime.fromisoformat(value.replace('Z', '+00:00'))


def q(values, p):
    return sorted(values)[max(0, math.ceil(len(values) * p) - 1)] if values else None


def distribution(values, scale=1):
    return {'count': len(values), 'sum': sum(values) / scale,
            'p50': q(values, .5) / scale if values else None,
            'p95': q(values, .95) / scale if values else None,
            'p99': q(values, .99) / scale if values else None,
            'max': max(values) / scale if values else None}


def scenario_key(s):
    return (s.get('PropertyID', s.get('property_id')), s.get('CheckIn', s.get('check_in'))[:10],
            s.get('CheckOut', s.get('check_out'))[:10], s.get('Adults', s.get('adults')))


def cell(value):
    return '' if value is None else str(value)


def write_csv(path, rows):
    with path.open('w', newline='') as f:
        writer = csv.DictWriter(f, fieldnames=list(rows[0]), lineterminator='\n')
        writer.writeheader()
        writer.writerows(rows)


def analyze(directory, output):
    summary, plan, db = (read(directory / name) for name in ['summary.json', 'plan.json', 'readback.json'])
    http, calls, samples = (read(directory / name) for name in ['http.json', 'calls.json', 'samples.json'])
    jobs = {j['id']: j for j in db['jobs']}
    runs = {r['id']: r for r in db['runs']}
    attempts = {(a['job_id'], a['attempt_no']): a for a in db['attempts']}
    observations, offers = collections.defaultdict(list), collections.defaultdict(list)
    for row in db['observations']:
        observations[row['attempt_id']].append(row)
    for row in db['offers']:
        offers[row['observation_id']].append(row)
    expected = {scenario_key(s) for scenarios in plan['scenarios'].values() for s in scenarios}
    actual = [scenario_key(j) for j in db['jobs']]
    assert len(expected) == len(actual) == len(set(actual)) == 610 and expected == set(actual)
    assert all(j['status'] in ('succeeded', 'partial', 'no_offers', 'failed', 'canceled') for j in db['jobs'])
    assert summary['before']['counts'] == {k: 0 for k in summary['before']['counts']}
    assert summary['after']['counts'] == summary['after_replay']['counts']
    assert summary['repeat_http'] == summary['repeat_calls'] == 0
    assert not summary['runtime_error'] and not summary['repeat_error']
    assert len(calls) == len(db['attempts']) == 610
    assert len(http) == plan['expected_http_without_retries'] == 976

    # All 35 CSV columns, including LEFT JOIN rows for categories without offers.
    audited_rows, audited_cells, csv_files = 0, 0, []
    for artifact in summary['csv']:
        body = pathlib.Path(artifact['path']).read_bytes()
        assert hashlib.sha256(body).hexdigest() == artifact['sha256'] and len(body) == artifact['bytes']
        with pathlib.Path(artifact['path']).open(newline='') as f:
            rows = list(csv.DictReader(f))
        assert len(rows) == artifact['rows'] and len(rows[0]) == 35
        expected_rows = {}
        for job in jobs.values():
            if job['run_id'] != artifact['run_id']:
                continue
            run = runs[job['run_id']]
            attempt = attempts.get((job['id'], job['attempts']), {})
            for ob in observations.get(attempt.get('id'), []) or [{}]:
                for offer in offers.get(ob.get('id'), []) or [{}]:
                    key = (job['id'], cell(ob.get('category_id')), cell(offer.get('variant_id')), cell(offer.get('rate_id')))
                    values = {'group_id': 'arena33', 'schema': plan['schema'], 'property_id': job['property_id'],
                              'source': job['source'], 'source_property_id': job['source_property_id'], 'run_id': run['id'],
                              'run_started_at': run['started_at'], 'run_finished_at': run['finished_at'], 'run_status': run['status'],
                              'job_id': job['id'], 'job_status': job['status'], 'check_in': job['check_in'], 'check_out': job['check_out'],
                              'los': job['los'], 'adults': job['adults'], 'rooms': job['rooms'], 'attempt_id': attempt.get('id'),
                              'attempt_no': attempt.get('attempt_no'), 'collected_at': attempt.get('collected_at'),
                              'attempt_started_at': attempt.get('started_at'), 'attempt_finished_at': attempt.get('finished_at'),
                              'attempt_outcome': attempt.get('outcome'), 'http_status': attempt.get('http_status'),
                              'error_code': attempt.get('error_code') or job['last_error_code'],
                              'category_id': ob.get('category_id'), 'category_name': ob.get('category_name'), 'area_m2': ob.get('area_m2'),
                              'stock_status': ob.get('stock_status'), 'available': ob.get('available'),
                              'variant_id': offer.get('variant_id'), 'rate_id': offer.get('rate_id'), 'rate_name': offer.get('rate_name'),
                              'price': offer.get('price'), 'currency': offer.get('currency'), 'reported_quantity': offer.get('reported_quantity')}
                    assert key not in expected_rows
                    expected_rows[key] = values
        assert len(expected_rows) == len(rows)
        seen = set()
        for row in rows:
            key = tuple(row[k] for k in ('job_id', 'category_id', 'variant_id', 'rate_id'))
            assert key not in seen
            seen.add(key)
            want = expected_rows[key]
            assert row.keys() == want.keys()
            for k, value in want.items():
                if k in ('run_started_at', 'run_finished_at', 'collected_at', 'attempt_started_at', 'attempt_finished_at') and value is not None:
                    assert timestamp(row[k]) == timestamp(value), (key, k)
                elif k in ('price', 'area_m2') and value is not None:
                    assert decimal.Decimal(row[k]) == decimal.Decimal(value), (key, k)
                else:
                    assert row[k] == cell(value), (key, k)
            audited_rows += 1
            audited_cells += len(row)
        csv_files.append({k: artifact[k] for k in ['property', 'run_id', 'bytes', 'rows', 'columns', 'sha256', 'duration_ns']})

    source_results = {}
    scenario_groups = collections.defaultdict(list)
    for j in jobs.values():
        scenario_groups[(j['property_id'], j['source'], j['adults'], j['los'])].append(j)
    norm, save = collections.defaultdict(list), collections.defaultdict(list)
    events = []
    for line in (directory / 'events.jsonl').read_text().splitlines():
        event = json.loads(line)
        if event.get('msg') == 'collection.normalization.measured':
            norm[event['job_id']].append(event['normalization_duration_ns'])
        if event.get('msg') == 'collection.persistence.measured':
            save[event['job_id']].append(event['save_attempt_duration_ns'])
        if event.get('msg') in ('bnovo.collect.completed', 'travelline.collect.completed'):
            events.append(event)
    assert sum(map(len, norm.values())) == sum(map(len, save.values())) == 610
    assert all(a['queue_wait_ms'] is not None and a['queue_wait_ms'] >= 0 for a in attempts.values())
    for source in ['bnovo', 'travelline']:
        hh = sorted((h for h in http if h['source'] == source), key=lambda h: h['start_ns'])
        cc = [c for c in calls if c['info']['Source'] == source]
        ee = [e for e in events if e.get('source', 'bnovo') == source]
        source_results[source] = {'jobs': len(cc), 'http_requests': len(hh), 'http_codes': dict(collections.Counter(str(h['status']) for h in hh)),
                                  'endpoints': dict(collections.Counter(h['endpoint'] for h in hh)), 'network_or_read_errors': sum(h['error'] for h in hh),
                                  'http_latency_ms': distribution([h['duration_ns'] for h in hh], 1e6), 'body_bytes': sum(h['bytes'] for h in hh),
                                  'transport_start_gap_ms': distribution([b['start_ns'] - a['start_ns'] for a, b in zip(hh, hh[1:])], 1e6),
                                  'min_transport_start_gap_ms': min(b['start_ns'] - a['start_ns'] for a, b in zip(hh, hh[1:])) / 1e6,
                                  'collect_latency_ms': distribution([c['duration_ns'] for c in cc], 1e6),
                                  'parser_phase_ms': distribution([e['parse_duration_ms'] for e in ee if 'parse_duration_ms' in e]),
                                  'adapter_http_phase_ms': distribution([e['http_duration_ms'] for e in ee if 'http_duration_ms' in e]),
                                  'result_statuses': dict(collections.Counter(c['status'] for c in cc)),
                                  'offer_conditions': {k: sum(c[k] for c in cc) for k in ['Offers', 'Prices', 'Meals', 'Cancellations', 'Prepayments']}}
    scenario_rows = []
    for key, jj in sorted(scenario_groups.items()):
        property_id, source, adults, los = key
        ids = {j['id'] for j in jj}
        aa = [a for a in attempts.values() if a['job_id'] in ids]
        aid = {a['id'] for a in aa}
        oo = [o for o in db['observations'] if o['attempt_id'] in aid]
        oid = {o['id'] for o in oo}
        ff = [f for f in db['offers'] if f['observation_id'] in oid]
        cc = [c for c in calls if c['info']['Property'] == property_id and c['info']['Request']['Adults'] == adults
              and (timestamp(c['info']['Request']['CheckOut']) - timestamp(c['info']['Request']['CheckIn'])).days == los]
        cid = {c['info']['ID'] for c in cc}
        hh = [h for h in http if h['call_id'] in cid]
        statuses = collections.Counter(j['status'] for j in jj)
        stock = collections.Counter(o['stock_status'] for o in oo)
        row = {'property': property_id, 'source': source, 'adults': adults, 'los': los, 'dates': len({j['check_in'] for j in jj}),
               'jobs': len(jj), **{k: statuses[k] for k in ['succeeded', 'partial', 'no_offers', 'failed', 'canceled']},
               'http_requests': len(hh), 'http_429': sum(h['status'] == 429 for h in hh), 'attempts': len(aa), 'observations': len(oo),
               'offers': len(ff), **{k: stock[k] for k in ['known', 'unknown', 'conflict']},
               'collect_p50_ms': q([c['duration_ns'] / 1e6 for c in cc], .5), 'collect_p95_ms': q([c['duration_ns'] / 1e6 for c in cc], .95),
               'collect_p99_ms': q([c['duration_ns'] / 1e6 for c in cc], .99),
               'queue_wait_p95_ms': q([a['queue_wait_ms'] for a in aa], .95),
               'durable_queue_age_p95_seconds': q([(timestamp(a['started_at']) - timestamp(jobs[a['job_id']]['created_at'])).total_seconds() for a in aa], .95),
               'normalization_p95_ms': q([v / 1e6 for j in jj for v in norm[j['id']]], .95),
               'save_attempt_p95_ms': q([v / 1e6 for j in jj for v in save[j['id']]], .95),
               'missing_area': sum(o['area_m2'] is None for o in oo), 'missing_price': sum(f['price'] is None for f in ff),
               'null_reported_quantity': sum(f['reported_quantity'] is None for f in ff)}
        assert row['dates'] == row['jobs'] == 61
        scenario_rows.append(row)
    stocks = collections.Counter(o['stock_status'] for o in db['observations'])
    warnings = collections.Counter(w['Code'] for o in db['observations'] for w in o['warnings'])
    metadata_repetition = {}
    for prop in sorted({h['property'] for h in http if h['endpoint'] == 'hotel_info'}):
        subset = [h for h in http if h['property'] == prop and h['endpoint'] == 'hotel_info']
        metadata_repetition[prop] = {'requests': len(subset), 'distinct_response_hashes': len({h['sha256'] for h in subset}), 'bytes': sum(h['bytes'] for h in subset)}
    result = {'wall_seconds': summary['wall_ns'] / 1e9, 'planning_seconds': summary['planning_ns'] / 1e9,
              'runtime_seconds': summary['runtime_ns'] / 1e9, 'finalization_seconds': summary['finalization_ns'] / 1e9,
              'http_span_seconds': (max(timestamp(h['finished']) for h in http) - min(timestamp(h['started']) for h in http)).total_seconds(),
              'job_statuses': dict(collections.Counter(j['status'] for j in jobs.values())), 'stocks': dict(stocks), 'warning_codes': dict(warnings),
              'sources': source_results, 'counts': summary['after']['counts'], 'before': summary['before'], 'after': summary['after'],
              'replay_http': summary['repeat_http'], 'replay_calls': summary['repeat_calls'],
              'duplicate_jobs': len(actual) - len(set(actual)), 'persisted_retries': len(db['attempts']) - len(db['jobs']),
              'unknown_observations': stocks['unknown'], 'known_zero': sum(o['stock_status'] == 'known' and o['available'] == 0 for o in db['observations']),
              'negative_queue_ages': sum(timestamp(a['started_at']) < timestamp(jobs[a['job_id']]['created_at']) for a in attempts.values()),
              'leases_still_set': sum(j['leased_until'] is not None for j in jobs.values()),
              'cpu_seconds': summary['cpu_seconds'], 'average_cpu_one_core_percent': summary['cpu_seconds'] / (summary['wall_ns'] / 1e9) * 100,
              'peak_cpu_one_second_percent': max(s['CPUPercent'] for s in samples), 'peak_rss_bytes': summary['peak_rss_bytes'],
              'peak_heap_sampled_bytes': max(s['HeapBytes'] for s in samples), 'peak_goroutines_sampled': max(s['Goroutines'] for s in samples),
              'goroutines_first_sample': samples[0]['Goroutines'], 'goroutines_last_sample': samples[-1]['Goroutines'],
              'peak_http': summary['peak_http'], 'peak_collect_calls': summary['peak_collect_calls'],
              'active_postgres_connections': None, 'postgres_sample_limitation': 'Observed binary lost application_name through ConnConfig.ConnString; zero samples are invalid, not zero connections. Harness corrected afterward without repeating HTTP.',
              'normalization_ms': distribution([v for values in norm.values() for v in values], 1e6),
              'save_attempt_ms': distribution([v for values in save.values() for v in values], 1e6),
              'batch_queue_wait_ms': distribution([a['queue_wait_ms'] for a in attempts.values()]),
              'durable_queue_age_seconds': distribution([(timestamp(a['started_at']) - timestamp(jobs[a['job_id']]['created_at'])).total_seconds() for a in attempts.values()]),
              'csv_files': csv_files, 'csv_audit': {'rows': audited_rows, 'columns': 35, 'cells': audited_cells, 'result': 'PASS'},
              'metadata_repetition': metadata_repetition, 'scenarios': scenario_rows}
    output.mkdir(parents=True, exist_ok=True)
    result['started_at'] = summary['started_at']
    result['finished_at'] = summary['finished_at']
    result['regular_subset'] = {k: sum(row[k] for row in scenario_rows if row['adults'] == row['los'] == 1)
                                for k in ['jobs', 'http_requests', 'succeeded', 'partial', 'no_offers', 'failed', 'known', 'unknown', 'offers']}
    result['unique_categories'] = len({(j['property_id'], o['category_id']) for j in jobs.values()
                                       for o in observations[attempts[(j['id'], j['attempts'])]['id']]})
    result['unique_rates_by_property'] = {p: len({f['rate_id'] for j in jobs.values() if j['property_id'] == p
                                                 for o in observations[attempts[(j['id'], j['attempts'])]['id']]
                                                 for f in offers[o['id']]}) for p in plan['scenarios']}
    result['currency_counts'] = dict(collections.Counter(f['currency'] for f in db['offers']))
    result['source_result_warning_codes'] = dict(collections.Counter(w for c in calls for w in c['WarningCodes'] or []))
    result['batches'] = [json.loads(line) for line in (directory / 'events.jsonl').read_text().splitlines()
                         if json.loads(line).get('msg') in ('collect.batch.completed', 'bnovo.batch.completed')]
    write_csv(output / 'http-metrics.csv', sorted(http, key=lambda h: h['start_ns']))
    # The measured process had an invalid connection tag; never publish its zeros as measurements.
    write_csv(output / 'process-samples.csv', [{k: v for k, v in s.items() if not k.startswith('DB')} for s in samples])
    attempt_rows, category_rows = [], collections.defaultdict(list)
    for j in jobs.values():
        a = attempts[(j['id'], j['attempts'])]
        attempt_rows.append({'job_id': j['id'], 'property': j['property_id'], 'source': j['source'],
                             'check_in': j['check_in'], 'check_out': j['check_out'], 'adults': j['adults'], 'los': j['los'],
                             'status': j['status'], 'started_at': a['started_at'], 'finished_at': a['finished_at'],
                             'duration_ms': a['duration_ms'], 'queue_wait_ms': a['queue_wait_ms'],
                             'durable_age_seconds': (timestamp(a['started_at']) - timestamp(j['created_at'])).total_seconds(),
                             'normalization_ns': norm[j['id']][0], 'save_attempt_ns': save[j['id']][0]})
        for ob in observations[a['id']]:
            category_rows[(j['property_id'], j['source'], j['adults'], j['los'], ob['category_id'], ob['category_name'])].append(ob)
    write_csv(output / 'attempt-metrics.csv', attempt_rows)
    write_csv(output / 'categories.csv', [dict(zip(['property', 'source', 'adults', 'los', 'category_id', 'category_name'], key),
        observations=len(oo), known=sum(o['stock_status'] == 'known' for o in oo),
        unknown=sum(o['stock_status'] == 'unknown' for o in oo), conflict=sum(o['stock_status'] == 'conflict' for o in oo),
        known_zero=sum(o['stock_status'] == 'known' and o['available'] == 0 for o in oo),
        missing_area=sum(o['area_m2'] is None for o in oo), offers=sum(len(offers[o['id']]) for o in oo))
        for key, oo in sorted(category_rows.items())])
    (output / 'results.json').write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n')
    write_csv(output / 'scenarios.csv', scenario_rows)
    print('PASS:', len(jobs), 'jobs;', audited_rows, 'CSV rows;', audited_cells, 'cells; no extra HTTP on replay')
    return result


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', required=True, type=pathlib.Path)
    parser.add_argument('--output', required=True, type=pathlib.Path)
    args = parser.parse_args()
    analyze(args.input, args.output)
