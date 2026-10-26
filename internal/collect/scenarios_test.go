package collect

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/config"
)

func TestCollectConfiguredBatch(t *testing.T) {
	for _, tc := range []struct {
		file   string
		adults config.AdultCounts
	}{
		{"arena33", config.AdultCounts{2}}, {"lindsay", config.AdultCounts{1, 2}},
	} {
		t.Run(tc.file, func(t *testing.T) {
			hotel, err := config.Load("../../configs/" + tc.file + ".json")
			if err != nil {
				t.Fatal(err)
			}
			before := append(config.AdultCounts(nil), hotel.Collection.Adults...)
			var calls atomic.Int32
			client := poolClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				adults, _ := strconv.Atoi(r.URL.Query().Get("adults"))
				if adults != 1 && adults != 2 {
					t.Errorf("adults=%d", adults)
				}
				fmt.Fprint(w, string(lindsayHTML(t, adults)))
			}, time.Second*3)
			results, summary, err := CollectConfiguredBatch(context.Background(), client, hotel, lindsayRequest(2).CheckIn, BatchOptions{Workers: 2})
			if err != nil {
				t.Fatal(err)
			}
			want := 3 * len(tc.adults)
			if len(results) != want || calls.Load() != int32(want) || summary.FailedCount != 0 {
				t.Fatalf("results=%d calls=%d summary=%+v", len(results), calls.Load(), summary)
			}
			for index, result := range results {
				nights := index%3 + 1
				adults := tc.adults[index/3]
				req := result.Request
				if req.Adults != adults || req.PropertyID != hotel.Source.PropertyID || req.CheckIn.Format("2006-01-02") != "2027-02-19" || !req.CheckOut.Equal(req.CheckIn.AddDate(0, 0, nights)) || req.CheckIn.Location().String() != hotel.Timezone {
					t.Errorf("scenario not retained: %+v", req)
				}
			}
			if !reflect.DeepEqual(hotel.Collection.Adults, before) {
				t.Fatal("configuration changed")
			}
		})
	}
}

func TestCollectConfiguredBatchInvalid(t *testing.T) {
	hotel, err := config.Load("../../configs/arena33.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*config.Config)
		date   time.Time
	}{
		{"unsupported_adults", func(c *config.Config) { c.Collection.Adults = config.AdultCounts{3} }, lindsayRequest(2).CheckIn},
		{"duplicate_adults", func(c *config.Config) { c.Collection.Adults = config.AdultCounts{2, 2} }, lindsayRequest(2).CheckIn},
		{"unsupported_los", func(c *config.Config) { c.Collection.Nights = []int{1, 2, 4} }, lindsayRequest(2).CheckIn},
		{"timezone", func(c *config.Config) { c.Timezone = "bad" }, lindsayRequest(2).CheckIn},
		{"date", func(*config.Config) {}, time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := hotel
			tc.change(&cfg)
			if _, _, err := CollectConfiguredBatch(context.Background(), nil, cfg, tc.date, BatchOptions{}); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
