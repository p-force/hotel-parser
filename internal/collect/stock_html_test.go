package collect

import (
	"bytes"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/source/bnovo"
	"golang.org/x/net/html"
)

func lindsayHTML(t *testing.T, adults int) []byte {
	t.Helper()
	body, err := os.ReadFile(fmt.Sprintf("testdata/lindsay-2027-02-19-adults-%d.html", adults))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func lindsayRequest(adults int) bnovo.SearchRequest {
	in := time.Date(2027, 2, 19, 0, 0, 0, 0, time.UTC)
	return bnovo.SearchRequest{PropertyID: "5125b29f-f39e-498a-94cf-9e47889ff248", CheckIn: in, CheckOut: in.AddDate(0, 0, 1), Adults: adults, Language: "ru"}
}

func TestLindsayScenarioStocks(t *testing.T) {
	for _, adults := range []int{1, 2} {
		t.Run(fmt.Sprint(adults), func(t *testing.T) {
			result, err := bnovo.Parse(lindsayHTML(t, adults), lindsayRequest(adults))
			if err != nil {
				t.Fatal(err)
			}
			expectedStatus := bnovo.SearchStatusSuccess
			if adults == 2 {
				expectedStatus = bnovo.SearchStatusPartial
			}
			if result.Status != expectedStatus || len(result.Categories) != 10 || len(result.Offers) != 28 {
				t.Fatalf("unexpected parsed result: %+v", result)
			}
			want := map[string]*int{"188497": stockInt(2), "188498": stockInt(2), "188499": stockInt(1), "188500": stockInt(2), "188501": stockInt(1), "188502": stockInt(2), "188503": stockInt(1), "188504": stockInt(0), "188505": stockInt(1), "201948": stockInt(1)}
			if adults == 2 {
				for id, count := range map[string]int{"188497": 2, "188498": 2, "188499": 1, "188500": 2, "188501": 1, "188502": 2, "188503": 1} {
					want[id] = stockInt(count)
				}
				want["188505"] = nil
			}
			before := stockSnapshot(t, result)
			for _, stock := range NormalizeCategoryStock(result) {
				expected, ok := want[stock.CategoryID]
				if !ok {
					t.Fatalf("invented category: %s", stock.CategoryID)
				}
				status := StockKnown
				if expected == nil {
					status = StockUnknown
				}
				if stock.Status != status || !reflect.DeepEqual(stock.Available, expected) {
					t.Errorf("%s: %+v, want %s/%v", stock.CategoryID, stock, status, expected)
				}
				if expected != nil && len(stock.Warnings) != 0 {
					t.Errorf("confirmed category has warnings: %+v", stock)
				}
				delete(want, stock.CategoryID)
			}
			if len(want) != 0 {
				t.Errorf("missing categories: %v", want)
			}
			if !bytes.Equal(before, stockSnapshot(t, result)) {
				t.Fatal("source result changed")
			}
			for _, offer := range result.Offers {
				if offer.CategoryID == "188505" && (offer.Visible || offer.VariantID != "188505" || offer.Availability.SelectDisabled || offer.Availability.DataAvailable == nil || *offer.Availability.DataAvailable != 1 || !reflect.DeepEqual(offer.Availability.SelectableQuantities, []int{1})) {
					t.Errorf("single-room evidence changed: %+v", offer)
				}
			}
		})
	}
}

func nodeAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
func visitHTML(n *html.Node, fn func(*html.Node)) {
	fn(n)
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		visitHTML(c, fn)
		c = next
	}
}

// Сохраняем исходную тарифную разметку Bnovo; удаляем только другие карточки.
func categoryHTML(t *testing.T, category string, change func(*html.Node)) []byte {
	t.Helper()
	doc, err := html.Parse(bytes.NewReader(lindsayHTML(t, 2)))
	if err != nil {
		t.Fatal(err)
	}
	visitHTML(doc, func(n *html.Node) {
		if id := nodeAttr(n, "data-roomtype-id"); id != "" && id != category {
			n.Parent.RemoveChild(n)
		}
	})
	visitHTML(doc, change)
	var body bytes.Buffer
	if err := html.Render(&body, doc); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}

func TestCollapsedBnovoOffers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		change  func(*html.Node)
		status  StockStatus
		count   *int
		warning string
		source  bnovo.SearchStatus
	}{
		{"two_hidden_rates", func(*html.Node) {}, StockKnown, stockInt(1), "", bnovo.SearchStatusSuccess},
		{"disabled", func(n *html.Node) {
			if n.Data == "select" {
				n.Attr = append(n.Attr, html.Attribute{Key: "disabled"})
			}
		}, StockUnknown, nil, "stock_evidence_missing", bnovo.SearchStatusPartial},
		{"conflicting_signals", func(n *html.Node) {
			if n.Data == "select" {
				for i := range n.Attr {
					if n.Attr[i].Key == "data-available" {
						n.Attr[i].Val = "2"
					}
				}
			}
		}, StockConflict, nil, "stock_quantity_conflict", bnovo.SearchStatusPartial},
		{"conflicting_rates", func(n *html.Node) {
			if n.Data == "select" && nodeAttr(n, "data-plan-id") == "772153" {
				for i := range n.Attr {
					if n.Attr[i].Key == "data-available" {
						n.Attr[i].Val = "2"
					}
				}
				option := &html.Node{Type: html.ElementNode, Data: "option", Attr: []html.Attribute{{Key: "value", Val: "2"}}}
				option.AppendChild(&html.Node{Type: html.TextNode, Data: "2"})
				n.AppendChild(option)
			}
		}, StockConflict, nil, "stock_quantity_conflict", bnovo.SearchStatusSuccess},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := categoryHTML(t, "188501", tc.change)
			if !strings.Contains(string(body), "d-none") {
				t.Fatal("fixture lost collapsed HTML")
			}
			result, err := bnovo.Parse(body, lindsayRequest(2))
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != tc.source {
				t.Errorf("source status=%s, warnings=%+v", result.Status, result.Warnings)
			}
			if len(result.Offers) != 2 {
				t.Fatalf("offers=%+v", result.Offers)
			}
			for _, offer := range result.Offers {
				if offer.Visible {
					t.Fatal("Visible must retain HTML display state")
				}
			}
			stocks := NormalizeCategoryStock(result)
			if len(stocks) != 1 || stocks[0].Status != tc.status || !reflect.DeepEqual(stocks[0].Available, tc.count) {
				t.Fatalf("stocks=%+v", stocks)
			}
			found := tc.warning == ""
			for _, w := range stocks[0].Warnings {
				found = found || w.Code == tc.warning
			}
			if !found {
				t.Errorf("warning %s missing: %+v", tc.warning, stocks[0].Warnings)
			}
			if tc.source == bnovo.SearchStatusSuccess {
				for _, w := range result.Warnings {
					if w.Code == "category_availability_unknown" {
						t.Errorf("collapsed category incorrectly unknown: %+v", w)
					}
				}
			}
		})
	}
}

func TestLindsayGuestVariants(t *testing.T) {
	for _, adults := range []int{1, 2} {
		t.Run(fmt.Sprint(adults), func(t *testing.T) {
			result, err := bnovo.Parse(lindsayHTML(t, adults), lindsayRequest(adults))
			if err != nil {
				t.Fatal(err)
			}
			for _, stock := range NormalizeCategoryStock(result) {
				if stock.CategoryID == "188500" && (stock.Status != StockKnown || stock.Available == nil || *stock.Available != 2) {
					t.Fatal(stock)
				}
				if stock.CategoryID == "188505" {
					if adults == 1 {
						if stock.Status != StockKnown || stock.Available == nil || *stock.Available != 1 {
							t.Fatal(stock)
						}
					} else if stock.Status != StockUnknown || stock.Available != nil {
						t.Fatal(stock)
					}
				}
			}
		})
	}
}

func TestReducedGuestCountRequiresRowConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name, value      string
		remove, disabled bool
		warning          string
	}{
		{name: "confirmed", value: "1"},
		{name: "not_confirmed", value: "0", warning: "stock_evidence_missing"},
		{name: "missing", remove: true, warning: "stock_evidence_missing"},
		{name: "invalid", value: "2", warning: "row_availability_invalid"},
		{name: "disabled", value: "1", disabled: true, warning: "stock_evidence_missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := categoryHTML(t, "188497", func(n *html.Node) {
				if n.Data == "tr" {
					attrs := n.Attr[:0]
					for _, a := range n.Attr {
						if a.Key == "data-available" {
							if tc.remove {
								continue
							}
							a.Val = tc.value
						}
						attrs = append(attrs, a)
					}
					n.Attr = attrs
				}
				if n.Data == "select" && tc.disabled {
					n.Attr = append(n.Attr, html.Attribute{Key: "disabled"})
				}
			})
			result, err := bnovo.Parse(body, lindsayRequest(1))
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Variants) != 1 || result.Variants[0].Adults != 2 {
				t.Fatalf("variant guest count changed: %+v", result.Variants)
			}
			stock := NormalizeCategoryStock(result)[0]
			if tc.warning == "" {
				if stock.Status != StockKnown || stock.Available == nil || *stock.Available != 2 || result.Status != bnovo.SearchStatusSuccess {
					t.Fatalf("stock=%+v source=%s", stock, result.Status)
				}
			} else {
				if stock.Status != StockUnknown || stock.Available != nil || result.Status != bnovo.SearchStatusPartial {
					t.Fatalf("stock=%+v source=%s", stock, result.Status)
				}
				found := false
				for _, w := range stock.Warnings {
					found = found || w.Code == tc.warning
				}
				if !found {
					t.Fatalf("missing warning %s: %+v", tc.warning, stock.Warnings)
				}
			}
		})
	}
}
