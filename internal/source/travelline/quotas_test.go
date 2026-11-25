package travelline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/p-force/hotel-parser/internal/source"
)

func quotaDocument(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}
func quotaJSON(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func quotaRoom(stay map[string]any) map[string]any {
	return stay["room_types"].([]any)[0].(map[string]any)
}

func TestRawQuotasIndependentOfOffers(t *testing.T) {
	info, body, req := fixture(t)
	baseline, err := Parse(info, body, req, publicRates)
	if err != nil {
		t.Fatal(err)
	}
	if baseline.RoomTypeQuotas == nil || len(baseline.RoomTypeQuotas.Items) != 8 {
		t.Fatal("missing full array")
	}
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
		rates  []string
	}{
		{"excluded rates", func(map[string]any) {}, []string{"excluded"}},
		{"invalid price", func(d map[string]any) {
			for _, s := range d["room_stays"].([]any) {
				s.(map[string]any)["total"].(map[string]any)["price_after_tax"] = -1
			}
		}, publicRates},
		{"wrong dates", func(d map[string]any) {
			for _, s := range d["room_stays"].([]any) {
				s.(map[string]any)["stay_dates"].(map[string]any)["start_date"] = "bad"
			}
		}, publicRates},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := quotaDocument(t, body)
			tc.change(doc)
			result, err := Parse(info, quotaJSON(t, doc), req, tc.rates)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Offers) != 0 || !reflect.DeepEqual(result.RoomTypeQuotas, baseline.RoomTypeQuotas) {
				t.Fatalf("filtered raw evidence: offers=%d quotas=%+v", len(result.Offers), result.RoomTypeQuotas)
			}
		})
	}
	for i, q := range baseline.RoomTypeQuotas.Items {
		if q.Ordinal != i || q.SourceRoomID == nil || q.MappingStatus != source.QuotaMapped {
			t.Fatalf("order/mapping: %+v", q)
		}
	}
}

func TestRawQuotasDuplicatesNullZeroAndUnmatched(t *testing.T) {
	info, body, req := fixture(t)
	doc := quotaDocument(t, body)
	stay := doc["room_stays"].([]any)[0].(map[string]any)
	rph := quotaRoom(stay)["room_type_quota_rph"].(string)
	category := quotaRoom(stay)["code"].(string)
	doc["room_stays"] = []any{stay}
	doc["room_type_quotas"] = []any{map[string]any{"rph": rph, "quantity": 10}, map[string]any{"rph": rph, "quantity": 10}, map[string]any{"rph": rph, "quantity": 0}, map[string]any{"rph": rph, "quantity": nil}, map[string]any{"rph": "orphan"}, map[string]any{"rph": "", "quantity": -1}}
	result, err := Parse(info, quotaJSON(t, doc), req, publicRates)
	if err != nil {
		t.Fatal(err)
	}
	items := result.RoomTypeQuotas.Items
	if len(items) != 6 {
		t.Fatal("array collapsed", items)
	}
	for i, q := range items {
		if q.Ordinal != i {
			t.Fatal("order changed")
		}
		if i < 4 && (q.SourceRoomID == nil || *q.SourceRoomID != category || q.MappingStatus != source.QuotaMapped) {
			t.Fatal("unambiguous category lost", q)
		}
	}
	if *items[0].Quantity != 10 || *items[1].Quantity != 10 || *items[2].Quantity != 0 || items[3].Quantity != nil || items[4].Quantity != nil || *items[5].Quantity != -1 {
		t.Fatal("quantity changed", items)
	}
	if items[4].MappingStatus != source.QuotaUnmapped || items[4].SourceRoomID != nil || items[5].SourceRoomID != nil {
		t.Fatal("guessed mapping")
	}
}

func TestRawQuotaMappingAmbiguity(t *testing.T) {
	for _, kind := range []string{"different category", "missing category", "foreign hotel", "duplicate reference", "multi room stay", "unknown metadata category"} {
		t.Run(kind, func(t *testing.T) {
			info, body, req := fixture(t)
			doc := quotaDocument(t, body)
			stay := doc["room_stays"].([]any)[0].(map[string]any)
			room := quotaRoom(stay)
			rph := room["room_type_quota_rph"].(string)
			category := room["code"].(string)
			clone := quotaDocument(t, quotaJSON(t, stay))
			expected := source.QuotaConflict
			switch kind {
			case "different category":
				quotaRoom(clone)["code"] = "different"
			case "missing category":
				quotaRoom(clone)["code"] = ""
			case "foreign hotel":
				clone["hotel_ref"].(map[string]any)["code"] = "other"
			case "duplicate reference":
				expected = source.QuotaMapped
			case "multi room stay":
				expected = source.QuotaMapped
				stay["room_types"] = []any{room, map[string]any{"code": "another", "room_type_quota_rph": "other"}}
			case "unknown metadata category":
				expected = source.QuotaMapped
				quotaRoom(stay)["code"] = "not-in-metadata"
				quotaRoom(clone)["code"] = "not-in-metadata"
				category = "not-in-metadata"
			}
			doc["room_stays"] = []any{stay, clone}
			doc["room_type_quotas"] = []any{map[string]any{"rph": rph, "quantity": 1}}
			result, err := Parse(info, quotaJSON(t, doc), req, publicRates)
			if err != nil {
				t.Fatal(err)
			}
			q := result.RoomTypeQuotas.Items[0]
			if q.MappingStatus != expected {
				t.Fatalf("mapping: %+v", q)
			}
			if expected == source.QuotaConflict && q.SourceRoomID != nil {
				t.Fatal("guessed category", q)
			}
			if expected == source.QuotaMapped && (q.SourceRoomID == nil || *q.SourceRoomID != category) {
				t.Fatal("missing category", q)
			}
		})
	}
}

func TestRawQuotaEmptyAbsentAndNoStays(t *testing.T) {
	info, body, req := fixture(t)
	for _, kind := range []string{"absent", "null", "empty", "no stays", "missing stays", "invalid metadata"} {
		t.Run(kind, func(t *testing.T) {
			doc := quotaDocument(t, body)
			metadata := info
			switch kind {
			case "absent":
				delete(doc, "room_type_quotas")
			case "null":
				doc["room_type_quotas"] = nil
			case "empty":
				doc["room_type_quotas"] = []any{}
			case "no stays":
				doc["room_stays"] = []any{}
			case "missing stays":
				delete(doc, "room_stays")
			case "invalid metadata":
				metadata = []byte("invalid")
			}
			result, err := Parse(metadata, quotaJSON(t, doc), req, publicRates)
			if kind == "missing stays" || kind == "invalid metadata" {
				if err == nil {
					t.Fatal("expected parse error")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if kind == "absent" || kind == "null" {
				if result.RoomTypeQuotas != nil {
					t.Fatal("invented array")
				}
				return
			}
			if result.RoomTypeQuotas == nil {
				t.Fatal("array lost")
			}
			if kind == "empty" {
				if result.RoomTypeQuotas.Items == nil || len(result.RoomTypeQuotas.Items) != 0 {
					t.Fatal("empty not preserved")
				}
				return
			}
			if len(result.RoomTypeQuotas.Items) != 8 {
				t.Fatal("quotas lost")
			}
			if kind == "no stays" || kind == "missing stays" {
				for _, q := range result.RoomTypeQuotas.Items {
					if q.SourceRoomID != nil || q.MappingStatus != source.QuotaUnmapped {
						t.Fatal("invented mapping")
					}
				}
			}
		})
	}
}

func TestRawQuotasSurviveOfferTypeErrors(t *testing.T) {
	info, body, req := fixture(t)
	base, err := Parse(info, body, req, publicRates)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"price type", "guest type", "broken room links", "malformed JSON"} {
		t.Run(kind, func(t *testing.T) {
			doc := quotaDocument(t, body)
			stay := doc["room_stays"].([]any)[0].(map[string]any)
			switch kind {
			case "price type":
				stay["total"].(map[string]any)["price_after_tax"] = "not-a-number"
			case "guest type":
				stay["guests"] = false
			case "broken room links":
				stay["room_types"] = false
			}
			encoded := quotaJSON(t, doc)
			if kind == "malformed JSON" {
				encoded = append(encoded, 'x')
			}
			result, err := Parse(info, encoded, req, publicRates)
			if err == nil {
				t.Fatal("expected unchanged parse failure")
			}
			if kind == "malformed JSON" {
				if result.RoomTypeQuotas != nil {
					t.Fatal("invalid JSON must not produce evidence")
				}
				return
			}
			if result.RoomTypeQuotas == nil || len(result.RoomTypeQuotas.Items) != 8 {
				t.Fatal("valid quotas lost to unrelated type error")
			}
			if kind == "broken room links" {
				for _, q := range result.RoomTypeQuotas.Items {
					if q.SourceRoomID != nil || q.MappingStatus != source.QuotaUnmapped {
						t.Fatal("guessed mapping")
					}
				}
			} else if !reflect.DeepEqual(result.RoomTypeQuotas, base.RoomTypeQuotas) {
				t.Fatal("offer type error changed quota evidence")
			}
		})
	}
}

func TestCollectKeepsQuotasWhenOfferCannotBeDecoded(t *testing.T) {
	info, body, req := fixture(t)
	doc := quotaDocument(t, body)
	doc["room_stays"].([]any)[0].(map[string]any)["total"].(map[string]any)["price_after_tax"] = "not-a-number"
	broken := quotaJSON(t, doc)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == infoPath {
			w.Write(info)
		} else {
			w.Write(broken)
		}
	}))
	defer server.Close()
	client, err := NewClient(server.URL, server.Client(), publicRates)
	if err != nil {
		t.Fatal(err)
	}
	result := client.Collect(context.Background(), req)
	if result.Status != source.Failed || result.Error == nil || result.Error.Code != "parse_error" || result.HTTPStatus != 200 || result.RoomTypeQuotas == nil || len(result.RoomTypeQuotas.Items) != 8 {
		t.Fatalf("partial decoded quota evidence lost: %+v", result)
	}
}
