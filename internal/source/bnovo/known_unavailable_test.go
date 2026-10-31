package bnovo

import (
	"os"
	"testing"
	"time"
)

func TestKnownDefectConfirmedUnavailableWithoutPlans(t *testing.T) {
	if os.Getenv("HOTEL_PARSER_REPRO_KNOWN_DEFECTS") != "1" {
		t.Skip("known defect reproduction; see Issue #35 verification report")
	}
	body := []byte(`<html><head><script>
const $enabledRoomTypes = {"1":{"id":1,"name":"Room"}};
const account_currency_iso4217 = "RUB";
</script></head><body><div class="room" data-roomtype-id="1"><div class="tariff__nodates">Здесь нет свободных мест с 30 декабря 2026 по 31 декабря 2026</div></div></body></html>`)
	checkIn := time.Date(2026, 12, 30, 0, 0, 0, 0, time.UTC)
	result, err := Parse(body, SearchRequest{PropertyID: "00000000-0000-0000-0000-000000000001", CheckIn: checkIn, CheckOut: checkIn.AddDate(0, 0, 1), Adults: 1, Language: "ru"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.CategoryAvailability) != 1 || !result.CategoryAvailability[0].ConfirmedUnavailable {
		t.Fatal("test did not reproduce exact-date explicit unavailability")
	}
	if result.Status != SearchStatusNoOffers {
		t.Fatalf("all categories explicitly unavailable, but status=%s warnings=%+v", result.Status, result.Warnings)
	}
}
