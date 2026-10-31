package travelline

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/p-force/hotel-parser/internal/source"
)

type reference struct {
	Code string `json:"code"`
}
type metadata struct {
	Hotels []struct {
		Code      string `json:"code"`
		Currency  string `json:"currency"`
		RoomTypes []struct {
			Code string `json:"code"`
			Name string `json:"name"`
			Kind string `json:"kind"`
			Size struct {
				Value float64 `json:"value"`
				Unit  string  `json:"unit"`
			} `json:"size"`
		} `json:"room_types"`
		RatePlans []struct {
			Code string `json:"code"`
			Name string `json:"name"`
		} `json:"rate_plans"`
	} `json:"hotels"`
	Errors   []json.RawMessage `json:"errors"`
	Warnings []json.RawMessage `json:"warnings"`
}
type availability struct {
	RoomStays []struct {
		Hotel  reference `json:"hotel_ref"`
		Guests []struct {
			Count     int `json:"count"`
			Placement struct {
				Kind string `json:"kind"`
			} `json:"placement"`
		} `json:"guests"`
		Rooms []struct {
			Code       string `json:"code"`
			Quota      string `json:"room_type_quota_rph"`
			Placements []struct {
				Code string `json:"code"`
			} `json:"placements"`
		} `json:"room_types"`
		Rates []struct {
			Code  string `json:"code"`
			Promo bool   `json:"promo"`
		} `json:"rate_plans"`
		Total struct {
			Price    json.Number `json:"price_after_tax"`
			Currency string      `json:"currency"`
		} `json:"total"`
		Dates struct {
			Start string `json:"start_date"`
			End   string `json:"end_date"`
		} `json:"stay_dates"`
	} `json:"room_stays"`
	Quotas []struct {
		RPH      string `json:"rph"`
		Quantity *int   `json:"quantity"`
	} `json:"room_type_quotas"`
	Diagnostics []json.RawMessage `json:"availability_result"`
	Errors      []json.RawMessage `json:"errors"`
	Warnings    []json.RawMessage `json:"warnings"`
}

var decimalPrice = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]+)?$`)

// Parse читает только подтверждённые поля BookingForm API. publicRates — явный
// список обычных тарифов: promo=false не доказывает отсутствие AZIMUT BONUS.
func Parse(info, body []byte, req source.SearchRequest, publicRates []string) (source.SearchResult, error) {
	result := source.SearchResult{Source: "travelline", Request: req, Status: source.Failed}
	var meta metadata
	var data availability
	if err := json.Unmarshal(info, &meta); err != nil {
		return result, fmt.Errorf("invalid TravelLine hotel metadata")
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return result, fmt.Errorf("invalid TravelLine availability")
	}
	if len(meta.Errors) > 0 || len(data.Errors) > 0 {
		return result, &source.CollectionError{Code: "source_error", Message: "TravelLine rejected search scenario"}
	}
	if len(meta.Hotels) != 1 || meta.Hotels[0].Code != req.PropertyID || len(meta.Hotels[0].RoomTypes) == 0 || data.RoomStays == nil {
		return result, fmt.Errorf("TravelLine response identity or structure invalid")
	}
	warn := func(code, category string) {
		result.Warnings = append(result.Warnings, source.ParseWarning{Code: code, Context: category, Message: "TravelLine response cannot fully confirm this category"})
	}
	if len(meta.Warnings) > 0 || len(data.Warnings) > 0 || len(data.Diagnostics) > 0 {
		warn("source_diagnostics", "")
	}
	categories := map[string]bool{}
	for _, room := range meta.Hotels[0].RoomTypes {
		if room.Code == "" || room.Name == "" || room.Kind != "room" || categories[room.Code] {
			return result, fmt.Errorf("TravelLine room metadata invalid")
		}
		categories[room.Code] = true
		category := source.RoomCategory{ID: room.Code, Name: room.Name}
		if room.Size.Unit == "square_metre" && room.Size.Value > 0 {
			area := room.Size.Value
			category.AreaM2 = &area
		}
		result.Categories = append(result.Categories, category)
	}
	sort.Slice(result.Categories, func(i, j int) bool { return result.Categories[i].ID < result.Categories[j].ID })
	names := map[string]string{}
	for _, rate := range meta.Hotels[0].RatePlans {
		if rate.Code == "" || names[rate.Code] != "" {
			return result, fmt.Errorf("TravelLine rate metadata invalid")
		}
		names[rate.Code] = rate.Name
	}
	allowed := map[string]bool{}
	for _, id := range publicRates {
		allowed[id] = true
	}
	quotas := map[string]*int{}
	badQuota := map[string]bool{}
	for _, q := range data.Quotas {
		if q.RPH == "" {
			warn("quota_reference_missing", "")
			continue
		}
		if previous, ok := quotas[q.RPH]; ok && (previous == nil || q.Quantity == nil || *previous != *q.Quantity) {
			badQuota[q.RPH] = true
		}
		quotas[q.RPH] = q.Quantity
	}
	quotaCategory := map[string]string{}
	for _, stay := range data.RoomStays {
		if len(stay.Rooms) == 1 {
			room := stay.Rooms[0]
			if previous, ok := quotaCategory[room.Quota]; ok && previous != room.Code {
				badQuota[room.Quota] = true
			} else {
				quotaCategory[room.Quota] = room.Code
			}
		}
	}
	variants := map[string]source.AccommodationVariant{}
	offers := map[string]source.RateOffer{}
	order := []string{}
	conflicts := map[string]bool{}
	for _, stay := range data.RoomStays {
		category := ""
		if len(stay.Rooms) == 1 {
			category = stay.Rooms[0].Code
		}
		if stay.Hotel.Code != req.PropertyID || !categories[category] || len(stay.Rates) != 1 {
			if stay.Hotel.Code != req.PropertyID || !categories[category] {
				category = ""
			}
			warn("offer_identity_invalid", category)
			continue
		}
		if !sameDate(stay.Dates.Start, req.CheckIn) || !sameDate(stay.Dates.End, req.CheckOut) {
			warn("offer_period_mismatch", category)
			continue
		}
		adults := 0
		validGuests := true
		for _, guest := range stay.Guests {
			if guest.Placement.Kind != "adult" || guest.Count <= 0 {
				validGuests = false
			}
			adults += guest.Count
		}
		if !validGuests || adults != req.Adults || len(req.ChildrenAges) > 0 {
			warn("offer_guests_mismatch", category)
			continue
		}
		rate := stay.Rates[0]
		// Отдельно опубликованные BONUS/промо-тарифы не становятся обычной ценой.
		if !allowed[rate.Code] || rate.Promo || strings.Contains(strings.ToLower(names[rate.Code]), "bonus") {
			continue
		}
		if names[rate.Code] == "" {
			warn("offer_rate_unknown", category)
			continue
		}
		if !decimalPrice.MatchString(string(stay.Total.Price)) || stay.Total.Currency != meta.Hotels[0].Currency || len(stay.Total.Currency) != 3 {
			warn("offer_price_invalid", category)
			continue
		}
		codes := []string{}
		validPlacement := true
		for _, placement := range stay.Rooms[0].Placements {
			if placement.Code == "" {
				validPlacement = false
			}
			codes = append(codes, placement.Code)
		}
		if !validPlacement || len(codes) == 0 {
			warn("offer_variant_missing", category)
			continue
		}
		sort.Strings(codes)
		variantID := category + ":" + strings.Join(codes, "+")
		variants[variantID] = source.AccommodationVariant{ID: variantID, CategoryID: category, Adults: adults}
		var quantity *int
		quota := stay.Rooms[0].Quota
		if badQuota[quota] {
			warn("stock_quantity_conflict", category)
		} else if value := quotas[quota]; value != nil && *value >= 0 {
			copy := *value
			quantity = &copy
		}
		offer := source.RateOffer{CategoryID: category, VariantID: variantID, RateID: rate.Code, RateName: names[rate.Code], Price: string(stay.Total.Price), Currency: stay.Total.Currency, Visible: true, Availability: source.AvailabilityEvidence{ReportedQuantity: quantity}}
		key := category + "/" + variantID + "/" + rate.Code
		if conflicts[key] {
			continue
		}
		if previous, ok := offers[key]; ok {
			if previous.Price != offer.Price || previous.Currency != offer.Currency || !equalQuantity(previous.Availability.ReportedQuantity, quantity) {
				delete(offers, key)
				conflicts[key] = true
				warn("offer_duplicate_conflict", category)
			}
			continue
		}
		offers[key] = offer
		order = append(order, key)
	}
	for _, key := range order {
		if offer, ok := offers[key]; ok {
			result.Offers = append(result.Offers, offer)
		}
	}
	for _, variant := range variants {
		result.Variants = append(result.Variants, variant)
	}
	sort.Slice(result.Variants, func(i, j int) bool { return result.Variants[i].ID < result.Variants[j].ID })
	for _, category := range result.Categories {
		state := source.CategoryAvailability{CategoryID: category.ID}
		for _, offer := range result.Offers {
			state.HasOffers = state.HasOffers || offer.CategoryID == category.ID
		}
		result.CategoryAvailability = append(result.CategoryAvailability, state)
	}
	result.Status = source.Success
	for _, stock := range NormalizeCategoryStock(result) {
		if stock.Status != source.StockKnown {
			warn("category_availability_unknown", stock.CategoryID)
		}
	}
	if len(result.Warnings) > 0 {
		result.Status = source.Partial
	}
	return result, nil
}

func sameDate(value string, date time.Time) bool {
	parsed, err := time.Parse("2006-01-02 15:04:05", value)
	return err == nil && parsed.Format("2006-01-02") == date.Format("2006-01-02")
}
func equalQuantity(a, b *int) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

// NormalizeCategoryStock использует quota quantity по ссылке конкретного room stay.
// Ни тарифы, ни варианты не суммируются. Отсутствие офферов/нулевая квота без
// подтверждённой семантики недоступности не превращаются в known=0.
func NormalizeCategoryStock(result source.SearchResult) []source.CategoryStock {
	stocks := make([]source.CategoryStock, 0, len(result.Categories))
	seen := map[string]bool{}
	for _, category := range result.Categories {
		if category.ID == "" || seen[category.ID] {
			continue
		}
		seen[category.ID] = true
		stock := source.CategoryStock{CategoryID: category.ID, Status: source.StockUnknown}
		blocked := (result.Status != source.Success && result.Status != source.Partial && result.Status != source.NoOffers) || result.Error != nil || !validPropertyID(result.Request.PropertyID) || result.Request.Adults < 1 || len(result.Request.ChildrenAges) != 0 || result.Request.CheckIn.IsZero() || result.Request.CheckOut.Format("2006-01-02") <= result.Request.CheckIn.Format("2006-01-02")
		conflict := false
		for _, w := range result.Warnings {
			if w.Context == "" || w.Context == category.ID {
				stock.Warnings = append(stock.Warnings, w)
				blocked = true
				if w.Code == "stock_quantity_conflict" {
					conflict = true
				}
			}
		}
		var count *int
		incomplete := false
		for _, offer := range result.Offers {
			if offer.CategoryID != category.ID || !offer.Visible || offer.Availability.SelectDisabled {
				continue
			}
			matches := false
			for _, variant := range result.Variants {
				if variant.ID == offer.VariantID && variant.CategoryID == category.ID && variant.Adults == result.Request.Adults {
					matches = true
				}
			}
			if !matches {
				incomplete = true
				continue
			}
			q := offer.Availability.ReportedQuantity
			if q == nil || *q <= 0 {
				incomplete = true
				continue
			}
			if count != nil && *count != *q {
				conflict = true
			}
			value := *q
			count = &value
		}
		if count != nil {
			if result.Status == source.NoOffers {
				conflict = true
			}
			for _, state := range result.CategoryAvailability {
				if state.CategoryID == category.ID && state.ConfirmedUnavailable {
					conflict = true
				}
			}
		}
		if conflict {
			stock.Status = source.StockConflict
		} else if !blocked && !incomplete && count != nil {
			stock.Status = source.StockKnown
			stock.Available = count
		}
		stocks = append(stocks, stock)
	}
	return stocks
}
