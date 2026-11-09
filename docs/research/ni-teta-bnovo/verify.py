"""Offline consistency checks for saved research projections; no network access."""
import csv
import json
import re
from datetime import datetime
from pathlib import Path
from urllib.parse import parse_qs, urlparse

ROOT = Path(__file__).resolve().parent


def read(name):
    return json.loads((ROOT / name).read_text())


parsed = read('parsed.json')
manifest = read('http-manifest.json')
html = read('html-signals.json')
metadata = {h['uid']: h for h in read('metadata.json')}
assert len(parsed) == len(manifest) == len(html) == 6
expected_dates = {'2026-11-17', '2026-12-04', '2027-01-15'}
seen = set()
count = 0
for item, http, raw in zip(parsed, manifest, html):
    result = item['result']
    req = result['Request']
    key = (req['PropertyID'], req['CheckIn'][:10])
    assert key not in seen
    seen.add(key)
    assert req['CheckIn'][:10] in expected_dates
    assert (datetime.fromisoformat(req['CheckOut'].replace('Z', '+00:00')) -
            datetime.fromisoformat(req['CheckIn'].replace('Z', '+00:00'))).days == 1
    assert req['Adults'] == 1 and not req['ChildrenAges'] and req['Language'] == 'ru'
    url = urlparse(http['url'])
    query = parse_qs(url.query)
    assert url.path.endswith(req['PropertyID']) and query['adults'] == ['1']
    assert datetime.strptime(query['dfrom'][0], '%d-%m-%Y').date().isoformat() == key[1]
    assert datetime.strptime(query['dto'][0], '%d-%m-%Y').date().isoformat() == req['CheckOut'][:10]
    assert query['lang'] == ['ru']
    assert result['Status'] == 'success' and result['Error'] is None and not result['Warnings']
    assert result['HTTPStatus'] == http['status'] == 200 and not http['error']
    assert raw['source_sha256'] == http['sha256']
    assert raw['source_file'] == http['raw_local_file'] and raw['identity']['uid'] == req['PropertyID']
    assert raw['currency'] == 'RUB'
    cats = {c['ID']: c for c in result['Categories']}
    raw_cats = {c['category_id']: c for c in raw['categories']}
    assert cats.keys() == raw_cats.keys()
    variants = {v['ID']: v for v in result['Variants']}
    assert len(cats) == len(variants) == (2 if item['hotel'] == 'teta' else 6)
    assert len(result['Offers']) == (8 if item['hotel'] == 'teta' else 18)
    stocks = {s['CategoryID']: s for s in item['normalized']}
    assert stocks.keys() == cats.keys()
    for category_id, category in cats.items():
        assert category['Name'] == raw_cats[category_id]['title']
        assert category['AreaM2'] == int(metadata[req['PropertyID']]['enabled_room_types'][category_id]['amenities']['1']['value'])
    raw_offers = {}
    for category in raw['categories']:
        for rate in category['rates']:
            for row in rate['rows']:
                for select in row['selects']:
                    attrs = select['attributes']
                    offer_key = (category['category_id'], attrs['data-room-id'], rate['rate_id'])
                    assert offer_key not in raw_offers
                    raw_offers[offer_key] = (rate, row, select)
    assert len(raw_offers) == len(result['Offers'])
    for offer in result['Offers']:
        offer_key = (offer['CategoryID'], offer['VariantID'], offer['RateID'])
        rate, row, select = raw_offers[offer_key]
        attrs = select['attributes']
        evidence = offer['Availability']
        guest_html = row['guest_list_html']
        counter = re.search(r'guest-list__counter">\s*x(\d+)', guest_html)
        adults = int(counter[1]) if counter else guest_html.count('bnovobook-icon--adult')
        assert variants[offer['VariantID']]['Adults'] == adults
        assert offer['Price'] in [a['data-tariff-price'] for a in row['price_attributes']]
        assert offer['Currency'] == 'RUB'
        assert evidence['DataAvailable'] == int(attrs['data-available'])
        assert evidence['DataAvailableForGuests'] == rate['attributes']['data-available-for-guests'] == '1'
        assert evidence['RowAvailable'] == (row['row_attributes']['data-available'] == '1')
        assert evidence['SelectDisabled'] == ('disabled' in attrs) is False
        quantities = [int(o['value']) for o in select['options'] if not o['disabled'] and int(o['value']) > 0]
        assert evidence['SelectableQuantities'] == quantities
        assert 'min' not in attrs and 'max' not in attrs and attrs['data-promo-code-applied'] == '0'
        assert offer['Visible'] == (not select['hidden_ancestors'])
        stock = stocks[offer['CategoryID']]
        assert stock['Status'] == 'known' and not stock['Warnings']
        assert stock['Available'] == evidence['DataAvailable'] == max(quantities)
        count += 1
with (ROOT / 'offers.csv').open(newline='') as f:
    rows = list(csv.DictReader(f))
assert len(rows) == count == 78
for row, offer in zip(rows, [o for item in parsed for o in item['result']['Offers']]):
    assert row['price'] == offer['Price'] and row['rate_id'] == offer['RateID']
    assert row['category_id'] == offer['CategoryID'] and row['currency'] == offer['Currency']
    for column, field in [('rate_name', 'RateName'), ('meal_plan', 'MealPlan'),
                          ('cancellation', 'Cancellation'), ('prepayment', 'Prepayment')]:
        assert row[column] == offer[field]
    assert row['data_available'] == str(offer['Availability']['DataAvailable'])
    assert row['initial_visible'] == str(offer['Visible'])
for replay in read('browser-replay.json'):
    assert not replay['error']
    assert all(c['visible'] and all(r['visible'] for r in c['rates']) for c in replay['after'])
print('PASS: 6 scenarios, 24 category observations, 78 offers; HTTP/DOM/parser/CSV/stock agree.')
