"""Offline audit of public, sanitized excluded-rate evidence. No network or database."""
import collections
import datetime
import decimal
import json
import pathlib
import re

ROOT = pathlib.Path(__file__).resolve().parent
PREVIOUS = ROOT.parent / 'travelline-full-collection'


def load(name):
    return json.loads((ROOT / name).read_text(), parse_float=decimal.Decimal)


def walk(value):
    if isinstance(value, dict):
        for key, item in value.items():
            assert key.lower() not in {
                'authorization', 'cookie', 'set-cookie', 'token', 'access_token',
                'guest_refs', 'ref', 'contact_info', 'session_id', 'email', 'phone',
            }, key
            walk(item)
    elif isinstance(value, list):
        for item in value:
            walk(item)


def main():
    offers = load('historical-offers.json')
    assert collections.Counter(x['rate_id'] for x in offers) == {
        '816956': 48, '951554': 56, '633705': 63,
    }
    import csv
    with (PREVIOUS / 'exclusions.csv').open() as file:
        excluded = {r['rate']: r for r in csv.DictReader(file)}
    manifest = {e['seq']: e for e in load('historical-manifest.json')}
    for rate in ['816956', '951554', '633705']:
        selected = [o for o in offers if o['rate_id'] == rate]
        assert sorted({o['date'] for o in selected}) == json.loads(excluded[rate]['dates'])
    for offer in offers:
        assert offer['promo'] is False
        assert offer['currency'] == 'RUB' and offer['adults'] == 1
        start = datetime.date.fromisoformat(offer['stay_dates']['start_date'][:10])
        end = datetime.date.fromisoformat(offer['stay_dates']['end_date'][:10])
        assert (end - start).days == 1
        assert start.isoformat() == offer['date']
        assert offer['price'] > 0
        assert offer['source_body_sha256'] == manifest[offer['source_seq']]['sha256']
        assert offer['reported_quantity'] is None or offer['reported_quantity'] >= 0
    live = load('live-api.json')
    for hotel, data in live.items():
        for date, response in data['availability'].items():
            assert not response['error_code']
            quotas = {q['rph']: q['quantity'] for q in response['room_type_quotas']}
            for stay in response['room_stays']:
                assert stay['hotel_ref']['code'] == hotel
                assert stay['stay_dates']['start_date'][:10] == date
                assert sum(g['count'] for g in stay['guests']) == 1
                assert stay['total']['currency'] == 'RUB'
                assert stay['room_types'][0]['room_type_quota_rph'] in quotas
    matches = load('dom-api-matches.json')
    assert len(matches) == 6
    assert collections.Counter(m['rate_id'] for m in matches) == {
        '816956': 2, '951554': 2, '633705': 2,
    }
    for match in matches:
        assert match['anonymous_offer_confirmed'] and match['checkout_not_attempted']
        assert match['adults'] == match['rooms'] == match['los'] == 1
        assert match['api_offer']['room_types'][0]['code'] == match['category_id']
        assert match['api_offer']['rate_plans'][0]['code'] == match['rate_id']
        assert match['price_matched_direct_api_and_browser'] in match['dom_card_text'].replace('\u2009', '')
        assert decimal.Decimal(match['price_matched_direct_api_and_browser']) == match['api_offer']['total']['price_after_tax']
        query = match['api_query']
        assert query['criterions[0].adults'] == '1'
        assert query['criterions[0].hotels[0].code'] == match['hotel']
        assert query['criterions[0].dates'].split(';')[0] == match['date']
    browser_files = ['browser-evidence.json', 'browser-details-evidence.json',
                     'browser-rates-evidence.json', 'browser-retry-evidence.json']
    browser_requests = [x for f in browser_files for row in load(f) for x in row['xhr']]
    assert len(browser_requests) == 76
    assert all(x['status'] == 200 for x in browser_requests)
    assert not any('/booking_create' in x['path'] for x in browser_requests)
    requests = load('live-manifest.json')
    assert len(requests) == 9
    assert all(e['http_status'] == 200 and not e['error_code'] for e in requests)
    starts = [datetime.datetime.fromisoformat(e['started_at']) for e in requests]
    assert all((b-a).total_seconds() >= 1 for a, b in zip(starts, starts[1:]))
    for path in ROOT.glob('*.json'):
        walk(json.loads(path.read_text()))
        assert not re.search(r'tlTracker|globalUserId|profileUserId|browserSessionId|intervalSession|conId=|guest_refs', path.read_text())
        assert not re.search(r'Bearer\s+[A-Za-z0-9]|OAuth\s+[A-Za-z0-9]', path.read_text())
    # The report's baseline comparisons are descriptive of the saved snapshot only.
    lower = collections.Counter(
        o['rate_id'] for o in offers
        if o['baseline_allowed_min_same_category_guests'] is not None
        and o['price'] < o['baseline_allowed_min_same_category_guests']
    )
    assert lower == {'816956': 13, '951554': 22}
    print('PASS: 167 historical offers, dates/counts, prices, quota links, 9 direct GETs, 76 browser GETs, 6 DOM/API matches, safe JSON fields.')


if __name__ == '__main__':
    main()
