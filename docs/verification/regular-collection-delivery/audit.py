"""Verify published CSVs; optionally compare every cell to a committed DB readback.

python3 .../audit.py --readback /tmp/regular-collection-delivery/readback.json
No HTTP, database writes or credentials are used.
"""
import argparse
import collections
import csv
import datetime
import decimal
import hashlib
import json
import pathlib

ROOT = pathlib.Path(__file__).resolve().parent
COLUMNS = ('group_id schema property_id source source_property_id run_id run_started_at '
           'run_finished_at run_status job_id job_status check_in check_out los adults rooms '
           'attempt_id attempt_no collected_at attempt_started_at attempt_finished_at '
           'attempt_outcome http_status error_code category_id category_name area_m2 '
           'stock_status available variant_id rate_id rate_name price currency reported_quantity').split()
TIMES = {'run_started_at', 'run_finished_at', 'collected_at', 'attempt_started_at', 'attempt_finished_at'}
NUMERIC = {'price', 'area_m2'}


def instant(v):
    return datetime.datetime.fromisoformat(v.replace('Z', '+00:00')) if v else None


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--readback', type=pathlib.Path)
    args = parser.parse_args()
    checks = json.loads((ROOT / 'checks.json').read_text())
    for name, expected in checks['artifacts'].items():
        raw = (ROOT / name).read_bytes()
        assert len(raw) == expected['bytes']
        assert hashlib.sha256(raw).hexdigest() == expected['sha256'], name
    summary = json.loads((ROOT / 'summary.json').read_text())
    db = json.loads(args.readback.read_text()) if args.readback else None
    total = cells = empty = zero = 0
    for item in summary['properties']:
        path = ROOT / item['csv_file']
        raw = path.read_bytes()
        assert hashlib.sha256(raw).hexdigest() == item['csv_sha256']
        assert len(raw) == item['csv_bytes']
        with path.open() as f:
            reader = csv.DictReader(f)
            assert reader.fieldnames == COLUMNS
            rows = list(reader)
        assert len(rows) == item['csv_rows']
        total += len(rows)
        assert len({r['job_id'] for r in rows}) == 61
        assert all(r['los'] == r['adults'] == r['rooms'] == '1' for r in rows)
        assert all(r['property_id'] == item['property_id'] and r['run_id'] == item['run_id'] for r in rows)
        if db:
            entry = next(e for e in db['runs'] if e['run']['Run']['ID'] == item['run_id'])
            run = entry['run']['Run']
            jobs = {d['job']['ID']: d for d in entry['jobs']}
            expected_row_count = 0
            expected_dates = {(datetime.date.fromisoformat(summary['check_in_first']) + datetime.timedelta(days=n)).isoformat() for n in range(61)}
            assert {d['job']['Scenario']['CheckIn'][:10] for d in jobs.values()} == expected_dates
            for detail in jobs.values():
                a = detail['attempts'][-1] if detail['attempts'] else {}
                observations = a.get('Observations') or []
                expected_row_count += sum(max(1, len(o['Offers'] or [])) for o in observations) if observations else 1
                assert len({o['CategoryID'] for o in observations}) == len(observations)
                all_offers = [(o['CategoryID'], f['VariantID'], f['RateID']) for o in observations for f in o['Offers'] or []]
                assert len(all_offers) == len(set(all_offers))
                sc = detail['job']['Scenario']
                assert sc['LOS'] == sc['Adults'] == sc['Rooms'] == 1 and sc['Language'] == 'ru'
                assert not sc['ChildrenAges']
                assert datetime.date.fromisoformat(sc['CheckOut'][:10]) - datetime.date.fromisoformat(sc['CheckIn'][:10]) == datetime.timedelta(days=1)
            assert expected_row_count == len(rows)
        for row in rows:
            empty += row['available'] == ''
            zero += row['available'] == '0'
            if row['stock_status'] == 'unknown':
                assert row['available'] == ''
            if row['rate_id']:
                assert row['price'] != '' and row['currency'] == 'RUB'
            if not row['rate_id']:
                assert row['price'] == row['currency'] == row['reported_quantity'] == ''
            if not db:
                continue
            detail = jobs[row['job_id']]
            j = detail['job']
            a = detail['attempts'][-1] if detail['attempts'] else {}
            sc = j['Scenario']
            o = next((o for o in a.get('Observations') or [] if o['CategoryID'] == row['category_id']), {})
            offer = next((f for f in o.get('Offers') or [] if f['RateID'] == row['rate_id'] and f['VariantID'] == row['variant_id']), {})
            want = dict(group_id='arena33', schema=db['schema'], property_id=run['PropertyID'],
                        source=sc['Source'], source_property_id=sc['SourcePropertyID'], run_id=run['ID'],
                        run_started_at=run['StartedAt'], run_finished_at=run['FinishedAt'], run_status=run['Status'],
                        job_id=j['ID'], job_status=j['Status'], check_in=sc['CheckIn'][:10], check_out=sc['CheckOut'][:10],
                        los=sc['LOS'], adults=sc['Adults'], rooms=sc['Rooms'], attempt_id=a.get('ID'),
                        attempt_no=a.get('Number'), collected_at=a.get('CollectedAt'),
                        attempt_started_at=a.get('StartedAt'), attempt_finished_at=a.get('FinishedAt'),
                        attempt_outcome=a.get('Outcome'), http_status=a.get('HTTPStatus'),
                        error_code=(a.get('Error') or {}).get('Code') or j['LastErrorCode'],
                        category_id=o.get('CategoryID'), category_name=o.get('CategoryName'), area_m2=o.get('AreaM2'),
                        stock_status=o.get('StockStatus'), available=o.get('Available'), variant_id=offer.get('VariantID'),
                        rate_id=offer.get('RateID'), rate_name=offer.get('RateName'), price=offer.get('Price'),
                        currency=offer.get('Currency'), reported_quantity=offer.get('ReportedQuantity'))
            assert list(want) == COLUMNS
            for name, v in want.items():
                if name in TIMES:
                    assert instant(row[name]) == instant(v), (name, j['ID'])
                elif name in NUMERIC:
                    assert (decimal.Decimal(row[name]) if row[name] else None) == (decimal.Decimal(str(v)) if v is not None else None), (name, j['ID'])
                else:
                    assert row[name] == ('' if v is None else str(v)), (name, j['ID'])
                cells += 1
    assert total == summary['csv_rows']
    result = {'rows': total, 'columns': 35, 'db_cells_checked': cells,
              'empty_available_cells': empty, 'zero_available_cells': zero, 'result': 'PASS'}
    print(json.dumps(result))


if __name__ == '__main__':
    main()
