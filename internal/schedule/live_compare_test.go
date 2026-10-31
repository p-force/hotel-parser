//go:build integration

package schedule

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/p-force/hotel-parser/internal/collect"
	"github.com/p-force/hotel-parser/internal/source/bnovo"
	"github.com/p-force/hotel-parser/internal/storage"
	"golang.org/x/net/html"
)

type rawOfferEvidence struct {
	PropertyID, CheckIn, CategoryID, VariantID, RateID string
	Price, Currency, DataAvailable, RowAvailable       string
	OptionValues                                       []int
	Disabled                                           bool
}
type liveChecks struct {
	AttemptsCompared, ObservationsCompared, OffersCompared, RawOffersCompared int
	TariffsWithCancellation, TariffsWithPrepayment                            int
	Mismatches                                                                []string
	RawSamples                                                                []rawOfferEvidence
}

func (c *liveChecks) merge(other liveChecks) {
	c.AttemptsCompared += other.AttemptsCompared
	c.ObservationsCompared += other.ObservationsCompared
	c.OffersCompared += other.OffersCompared
	c.RawOffersCompared += other.RawOffersCompared
	c.TariffsWithCancellation += other.TariffsWithCancellation
	c.TariffsWithPrepayment += other.TariffsWithPrepayment
	c.Mismatches = append(c.Mismatches, other.Mismatches...)
	c.RawSamples = append(c.RawSamples, other.RawSamples...)
}

func verifyLiveAttempt(t *testing.T, r Registry, requests []*liveRequest, job storage.Job, a storage.Attempt, checks *liveChecks) bool {
	t.Helper()
	var source *liveRequest
	for _, q := range requests {
		if q.UID == job.Scenario.SourcePropertyID && q.CheckIn == job.Scenario.CheckIn.Format("02-01-2006") && q.CheckOut == job.Scenario.CheckOut.Format("02-01-2006") && q.Adults == strconv.Itoa(job.Scenario.Adults) && q.HTTPStatus == 200 && len(q.body) > 0 && a.CollectedAt != nil && !q.StartedAt.Before(*a.CollectedAt) && !q.StartedAt.After(a.FinishedAt) {
			source = q
			break
		}
	}
	if source == nil {
		return false
	}
	fail := func(field string) {
		value := fmt.Sprintf("%s/%s/%s: %s", job.Scenario.PropertyID, source.CheckIn, a.ID, field)
		checks.Mismatches = append(checks.Mismatches, value)
		t.Error("source/DB mismatch:", value)
	}
	req := bnovo.SearchRequest{PropertyID: job.Scenario.SourcePropertyID, CheckIn: job.Scenario.CheckIn, CheckOut: job.Scenario.CheckOut, Adults: job.Scenario.Adults, Language: job.Scenario.Language}
	parsed, err := bnovo.Parse(source.body, req)
	if err != nil {
		fail("parse replay failed")
		return false
	}
	for _, p := range r.Groups[0].Properties {
		if p.Hotel.ID == job.Scenario.PropertyID {
			collect.ValidateExpectedCategories(&parsed, p.Hotel.Inventory)
		}
	}
	if string(a.Outcome) != string(parsed.Status) {
		fail("outcome")
	}
	if len(parsed.Categories) != len(a.Observations) {
		fail("category count")
	}
	checks.AttemptsCompared++
	stocks := map[string]collect.CategoryStock{}
	for _, s := range collect.NormalizeCategoryStock(parsed) {
		stocks[s.CategoryID] = s
	}
	categories := map[string]bnovo.RoomCategory{}
	for _, c := range parsed.Categories {
		categories[c.ID] = c
	}
	variants := map[string]bnovo.AccommodationVariant{}
	for _, v := range parsed.Variants {
		variants[v.CategoryID+"/"+v.ID] = v
	}
	offers := map[string]bnovo.RateOffer{}
	for _, o := range parsed.Offers {
		offers[o.CategoryID+"/"+o.VariantID+"/"+o.RateID] = o
		if o.Cancellation != "" {
			checks.TariffsWithCancellation++
		}
		if o.Prepayment != "" {
			checks.TariffsWithPrepayment++
		}
	}
	doc, err := html.Parse(bytes.NewReader(source.body))
	if err != nil {
		fail("independent DOM parse")
		return false
	}
	raw := rawOffers(doc)
	currency := ""
	match := regexp.MustCompile(`account_currency_iso4217\s*=\s*["']([A-Z]{3})["']`).FindSubmatch(source.body)
	if len(match) > 1 {
		currency = string(match[1])
	}
	roomMeta := map[string]struct {
		Name      string `json:"name"`
		Amenities map[string]struct {
			Value string `json:"value"`
		} `json:"amenities"`
	}{}
	idx := regexp.MustCompile(`\$enabledRoomTypes\s*=`).FindIndex(source.body)
	if idx != nil {
		_ = json.NewDecoder(bytes.NewReader(source.body[idx[1]:])).Decode(&roomMeta)
	}
	for _, o := range a.Observations {
		checks.ObservationsCompared++
		s := stocks[o.CategoryID]
		c := categories[o.CategoryID]
		if !reflect.DeepEqual(o.Available, s.Available) || string(o.StockStatus) != string(s.Status) || !reflect.DeepEqual(o.AreaM2, c.AreaM2) || o.CategoryName != c.Name {
			fail("category identity/area/stock")
		}
		if meta, exists := roomMeta[o.CategoryID]; exists {
			if o.CategoryName != meta.Name {
				fail("raw category name")
			}
			if text := meta.Amenities["1"].Value; text != "" {
				area, err := strconv.ParseFloat(strings.ReplaceAll(text, ",", "."), 64)
				if err == nil && (o.AreaM2 == nil || *o.AreaM2 != area) {
					fail("raw area")
				}
			}
		}
		for _, state := range parsed.CategoryAvailability {
			if state.CategoryID == o.CategoryID && (state.HasOffers != o.HasOffers || state.ConfirmedUnavailable != o.ConfirmedUnavailable) {
				fail("category availability flags")
			}
		}
		for _, v := range o.Variants {
			source := variants[o.CategoryID+"/"+v.ID]
			if source.ID != v.ID || source.Adults != v.Adults {
				fail("variant identity/adults")
			}
		}
		for _, offer := range o.Offers {
			checks.OffersCompared++
			key := o.CategoryID + "/" + offer.VariantID + "/" + offer.RateID
			p, exists := offers[key]
			if !exists {
				fail("extra offer key")
				continue
			}
			delete(offers, key)
			if offer.Price != nil && !sameDecimal(*offer.Price, p.Price) {
				fail("price")
			}
			if (offer.Price == nil) != (p.Price == "") {
				fail("price presence")
			}
			if offer.Currency != nil && *offer.Currency != p.Currency {
				fail("currency")
			}
			if offer.RateName != p.RateName || offer.MealPlan != p.MealPlan || offer.Visible != p.Visible || offer.SelectDisabled != p.Availability.SelectDisabled || !reflect.DeepEqual(offer.DataAvailable, p.Availability.DataAvailable) || !reflect.DeepEqual(offer.RowAvailable, p.Availability.RowAvailable) || !slices.Equal(offer.SelectableQuantities, p.Availability.SelectableQuantities) {
				fail("offer identity/availability")
			}
			if rawRow, ok := raw[key]; ok {
				checks.RawOffersCompared++
				if offer.Price != nil && rawRow.Price != "" && !sameDecimal(*offer.Price, rawRow.Price) {
					fail("raw DOM price")
				}
				if offer.Currency != nil && currency != "" && *offer.Currency != currency {
					fail("raw currency")
				}
				if offer.DataAvailable != nil && strconv.Itoa(*offer.DataAvailable) != rawRow.DataAvailable {
					fail("raw DOM data-available")
				}
				if offer.SelectDisabled != rawRow.Disabled || !slices.Equal(offer.SelectableQuantities, rawRow.OptionValues) {
					fail("raw DOM options/disabled")
				}
				if len(checks.RawSamples) < 26 && offer.Visible {
					rawRow.PropertyID = job.Scenario.PropertyID
					rawRow.CheckIn = job.Scenario.CheckIn.Format("2006-01-02")
					rawRow.Currency = currency
					checks.RawSamples = append(checks.RawSamples, rawRow)
				}
			} else {
				fail("raw DOM offer key missing")
			}
		}
	}
	if len(offers) != 0 {
		fail("offers lost in persistence")
	}
	return true
}
func sameDecimal(a, b string) bool {
	x, ok := new(big.Rat).SetString(a)
	if !ok {
		return false
	}
	y, ok := new(big.Rat).SetString(b)
	return ok && x.Cmp(y) == 0
}
func attribute(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
func hasAttribute(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if a.Key == key {
			return true
		}
	}
	return false
}
func walkRaw(n *html.Node, visit func(*html.Node)) {
	visit(n)
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walkRaw(c, visit)
	}
}
func rawOffers(doc *html.Node) map[string]rawOfferEvidence {
	result := map[string]rawOfferEvidence{}
	walkRaw(doc, func(n *html.Node) {
		if n.Type != html.ElementNode || n.Data != "select" || !strings.Contains(" "+attribute(n, "class")+" ", " selectAvailable ") {
			return
		}
		r := rawOfferEvidence{CategoryID: attribute(n, "data-real-room-id"), VariantID: attribute(n, "data-room-id"), RateID: attribute(n, "data-plan-id"), DataAvailable: attribute(n, "data-available"), Disabled: hasAttribute(n, "disabled"), OptionValues: []int{}}
		for parent := n.Parent; parent != nil; parent = parent.Parent {
			if parent.Data == "tr" {
				r.RowAvailable = attribute(parent, "data-available")
				walkRaw(parent, func(p *html.Node) {
					if price := attribute(p, "data-tariff-price"); price != "" {
						r.Price = price
					}
				})
				break
			}
		}
		walkRaw(n, func(option *html.Node) {
			if option.Data != "option" || hasAttribute(option, "disabled") {
				return
			}
			v, err := strconv.Atoi(attribute(option, "value"))
			if err == nil && v > 0 {
				r.OptionValues = append(r.OptionValues, v)
			}
		})
		sort.Ints(r.OptionValues)
		r.OptionValues = slices.Compact(r.OptionValues)
		result[r.CategoryID+"/"+r.VariantID+"/"+r.RateID] = r
	})
	return result
}
