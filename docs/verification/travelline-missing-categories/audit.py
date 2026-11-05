"""Offline audit of scoped TravelLine research; never opens network or PostgreSQL."""
import collections
import datetime
import decimal
import json
import pathlib
import re

ROOT = pathlib.Path(__file__).resolve().parent
REPO = ROOT.parents[2]
CONFIG = {'5787': 'petroff', '585': 'aerostar', '333': 'sovietsky'}


def load(name):
    return json.loads((ROOT / name).read_text(), parse_float=decimal.Decimal)


def key(c):
    return f"{c['hotel']}-availability-{c['check_in']}-a{c['adults']}-los{c['los']}"


def safe(value):
    if isinstance(value, dict):
        for name, item in value.items():
            assert name.lower() not in {'authorization', 'cookie', 'set-cookie',
                                       'access_token', 'guest_refs', 'ref', 'contact_info'}, name
            safe(item)
    elif isinstance(value, list):
        for item in value:
            safe(item)


def main():
    cases = load('scenarios.json')
    assert len(cases) == 16
    assert collections.Counter(c['provenance'] for c in cases) == {
        'live': 14, 'historical 2026-11-03': 2,
    }
    parsed = {r['key']: r for r in load('parsed.json')}
    stored = load('postgres-readback.json')
    persisted = {r['key']: r for r in stored['cases']}
    meta = load('parser-metadata.json')
    matched = 0
    for c in cases:
        name = key(c)
        p = parsed[name]['result']
        saved = persisted[name]['attempt']
        scenario = persisted[name]['job']['Scenario']
        assert scenario['LOS'] == c['los'] and scenario['Adults'] == c['adults']
        assert p['Request']['PropertyID'] == c['hotel']
        assert p['Request']['CheckIn'][:10] == c['check_in']
        end = datetime.date.fromisoformat(c['check_in']) + datetime.timedelta(days=c['los'])
        assert p['Request']['CheckOut'][:10] == end.isoformat()
        assert saved['Outcome'] == p['Status']
        config = json.loads((REPO / 'configs' / (CONFIG[c['hotel']] + '.json')).read_text())
        allowed = {r['id'] for r in config['rates'] if r['enabled']}
        names = {r['code']: r['name'] for r in meta[c['hotel']]['hotels'][0]['rate_plans']}
        data = c['availability']
        quotas = {r['rph']: r['quantity'] for r in data['room_type_quotas']}
        expected = {}
        for stay in data['room_stays']:
            room, rate = stay['room_types'][0], stay['rate_plans'][0]
            if rate['code'] not in allowed or rate.get('promo') or 'bonus' in names[rate['code']].lower():
                continue
            assert stay['hotel_ref']['code'] == c['hotel']
            assert stay['stay_dates']['start_date'][:10] == c['check_in']
            assert stay['stay_dates']['end_date'][:10] == end.isoformat()
            assert sum(g['count'] for g in stay['guests']) == c['adults']
            variant = room['code'] + ':' + '+'.join(sorted(p['code'] for p in room['placements']))
            expected[(room['code'], variant, rate['code'])] = (
                stay['total']['price_after_tax'], stay['total']['currency'],
                quotas[room['room_type_quota_rph']],
            )
        actual = {(o['CategoryID'], o['VariantID'], o['RateID']):
                  (decimal.Decimal(o['Price']), o['Currency'], o['Availability']['ReportedQuantity'])
                  for o in p['Offers'] or []}
        assert actual == expected, name
        readback = {(obs['CategoryID'], o['VariantID'], o['RateID']):
                    (decimal.Decimal(o['Price']), o['Currency'], o.get('ReportedQuantity'))
                    for obs in saved['Observations'] for o in obs['Offers'] or []}
        assert readback == expected, name
        stocks = {s['CategoryID']: (s['Status'], s['Available']) for s in parsed[name]['normalized']}
        assert {o['CategoryID']: (o['StockStatus'], o['Available'])
                for o in saved['Observations']} == stocks
        if c['hotel'] == '585':
            assert not any(s['room_types'][0]['code'] == '250899' for s in data['room_stays'])
            assert stocks['250899'] == ('unknown', None)
        if c['hotel'] == '333':
            assert stocks['2992'] == ('known', 1)
            assert {k[2] for k in expected if k[0] == '2992'} == {'38564', '49218'}
        matched += len(expected)
    assert matched == stored['matched_offers'] == 360
    assert stored['counts'] == {'scenario_jobs': 16, 'collection_attempts': 16,
                               'observations': 126, 'observation_variants': 104,
                               'observation_offers': 360, 'csv_deliveries': 0}
    assert stored['external_http_requests'] == stored['replay_http_requests'] == 0
    assert stored['fixture_http_requests'] == 32 and stored['schema_removed_after_check']
    manifest = load('live-manifest.json')
    assert len(manifest) == 29 and all(e['status'] == 200 for e in manifest)
    starts = [datetime.datetime.fromisoformat(e['started_at']) for e in manifest]
    assert all((b-a).total_seconds() >= 1 for a, b in zip(starts, starts[1:]))
    dom = load('dom-confirmations.json')
    assert len(dom) == 9
    for c in dom:
        if c['hotel'] == '585':
            assert c['category_id'] == '250899' and c['site_selector'] == '101'
            assert c['message'] == 'Номера закончились.' and c['quantity'] is None
        elif c.get('api_offers'):
            assert decimal.Decimal(c['dom_public_min_price']) == min(o['price'] for o in c['api_offers'])
    for path in ROOT.glob('*.json'):
        data = json.loads(path.read_text())
        safe(data)
        assert not re.search(r'tlTracker|globalUserId|browserSessionId|intervalSession|conId=|Bearer\s+[A-Za-z0-9]|OAuth\s+[A-Za-z0-9]', path.read_text())
    print('PASS: 16 scenarios, 360 raw/Parse/PG prices and quotas, 126 stocks, 29 live GETs, 9 DOM category projections; privacy checks.')


if __name__ == '__main__':
    main()
