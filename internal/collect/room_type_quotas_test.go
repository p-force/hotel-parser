package collect

import (
	"context"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/source"
)

func TestStorageAttemptKeepsRawQuotaEvidenceWithoutOffers(t *testing.T) {
	quantity, category := 0, "cat"
	set := &RoomTypeQuotaSet{Items: []RoomTypeQuota{{Ordinal: 0, RPH: "a", Quantity: &quantity, SourceRoomID: &category, MappingStatus: source.QuotaMapped}, {Ordinal: 1, RPH: "a", MappingStatus: source.QuotaConflict}}}
	now := time.Now()
	result := source.SearchResult{Source: "travelline", Status: source.Partial, HTTPStatus: 200, RoomTypeQuotas: set}
	input, err := storageAttemptContext(context.Background(), result, "job", 1, now, now, 0, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(input.Observations) != 0 || input.RoomTypeQuotas == nil || len(input.RoomTypeQuotas.Items) != 2 {
		t.Fatal("evidence requires offer")
	}
	quantity = 99
	category = "changed"
	if *input.RoomTypeQuotas.Items[0].Quantity != 0 || *input.RoomTypeQuotas.Items[0].SourceRoomID != "cat" {
		t.Fatal("input aliased")
	}
	if storedRoomTypeQuotas(nil) != nil {
		t.Fatal("invented set")
	}
	empty := storedRoomTypeQuotas(&RoomTypeQuotaSet{Items: []RoomTypeQuota{}})
	if empty == nil || empty.Items == nil {
		t.Fatal("empty lost")
	}
}
