package collect

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/source/bnovo"
)

func TestCollectGuestScenarioHTTPAndPrices(t *testing.T) {
	one := string(lindsayHTML(t, 1))
	// Разные цены в локальных ответах делают смешивание сценариев наблюдаемым.
	one = strings.Replace(one, `data-tariff-price="3200"`, `data-tariff-price="3100"`, 1)
	bodies := map[int]string{1: one, 2: string(lindsayHTML(t, 2))}
	var calls atomic.Int32
	client := poolClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		adults, err := strconv.Atoi(r.URL.Query().Get("adults"))
		if err != nil || bodies[adults] == "" {
			t.Errorf("unexpected adults query: %q", r.URL.RawQuery)
			w.WriteHeader(400)
			return
		}
		if r.URL.Query().Get("dfrom") != "19-02-2027" || r.URL.Query().Get("dto") != "20-02-2027" || r.URL.Query().Get("lang") != "ru" {
			t.Errorf("wrong scenario query: %s", r.URL.RawQuery)
		}
		fmt.Fprint(w, bodies[adults])
	}, 3*time.Second)
	results, _, err := CollectBatch(context.Background(), client, []bnovo.SearchRequest{lindsayRequest(1), lindsayRequest(2)}, BatchOptions{Workers: 2})
	if err != nil {
		t.Fatal(err)
	}
	for i, result := range results {
		adults := i + 1
		expected, err := bnovo.Parse([]byte(bodies[adults]), lindsayRequest(adults))
		if err != nil {
			t.Fatal(err)
		}
		if result.HTTPStatus != 200 || result.Error != nil || result.Request.Adults != adults || !reflect.DeepEqual(result.Offers, expected.Offers) {
			t.Fatalf("scenario %d changed offers/prices: %+v", adults, result)
		}
		wantPrice := "3200"
		if adults == 1 {
			wantPrice = "3100"
		}
		found := false
		for _, offer := range result.Offers {
			if offer.CategoryID == "188497" && offer.RateID == "772150" {
				found = true
				if offer.Price != wantPrice {
					t.Errorf("adults=%d price=%s want=%s", adults, offer.Price, wantPrice)
				}
			}
		}
		if !found {
			t.Fatal("rate missing")
		}
		for _, stock := range NormalizeCategoryStock(result) {
			if stock.CategoryID == "188497" && (stock.Status != StockKnown || stock.Available == nil || *stock.Available != 2) {
				t.Fatal(stock)
			}
		}
	}
	if calls.Load() != 2 {
		t.Errorf("HTTP calls=%d want 2", calls.Load())
	}
}
