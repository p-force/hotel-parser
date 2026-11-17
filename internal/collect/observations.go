package collect

import (
	"fmt"
	"time"

	"github.com/p-force/hotel-parser/internal/source/bnovo"
)

// StockObservation сохраняет исходный сценарий: число взрослых и даты, определяющие LOS.
type StockObservation struct {
	Request bnovo.SearchRequest
	Stock   CategoryStock
}

// CategoryStockObservation объединяет только один отель, даты, язык и состав детей.
// Числа не суммируются; расхождение достоверных наблюдений даёт conflict без Available.
type CategoryStockObservation struct {
	PropertyID   string
	CheckIn      time.Time
	CheckOut     time.Time
	CategoryID   string
	Available    *int
	Status       StockStatus
	Observations []StockObservation
}

// AggregateCategoryStock сохраняет все сценарные наблюдения в порядке входа.
// unknown не подтверждает количество; известные наблюдения объединяются только при согласии.
func AggregateCategoryStock(results []bnovo.SearchResult) []CategoryStockObservation {
	type key struct{ property, in, out, language, children, category string }
	indices := make(map[key]int)
	groups := make([]CategoryStockObservation, 0)
	for _, result := range results {
		for _, stock := range NormalizeCategoryStock(result) {
			req := result.Request
			k := key{req.PropertyID, req.CheckIn.Format("2006-01-02"), req.CheckOut.Format("2006-01-02"), req.Language, fmt.Sprint(req.ChildrenAges), stock.CategoryID}
			index, ok := indices[k]
			if !ok {
				index = len(groups)
				indices[k] = index
				groups = append(groups, CategoryStockObservation{PropertyID: req.PropertyID, CheckIn: req.CheckIn, CheckOut: req.CheckOut, CategoryID: stock.CategoryID, Status: StockUnknown})
			}
			group := &groups[index]
			req.ChildrenAges = append([]int(nil), req.ChildrenAges...)
			group.Observations = append(group.Observations, StockObservation{Request: req, Stock: stock})
			if stock.Status == StockConflict || (stock.Status == StockKnown && stock.Available != nil && group.Available != nil && *stock.Available != *group.Available) {
				group.Status, group.Available = StockConflict, nil
			} else if group.Status != StockConflict && stock.Status == StockKnown && stock.Available != nil {
				value := *stock.Available
				group.Status, group.Available = StockKnown, &value
			}
		}
	}
	return groups
}
