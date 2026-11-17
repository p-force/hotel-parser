package collect

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/p-force/hotel-parser/internal/config"
	"github.com/p-force/hotel-parser/internal/source"
	"github.com/p-force/hotel-parser/internal/source/bnovo"
	"github.com/p-force/hotel-parser/internal/source/travelline"
)

// NewSourceClient создаёт один клиент источника, переиспользуемый всем runtime.
func NewSourceClient(cfg config.Config) (source.Client, error) {
	switch cfg.Source.Type {
	case "bnovo":
		return bnovo.NewClient(cfg.Source.BaseURL, nil)
	case "travelline":
		ids := []string{}
		for _, rate := range cfg.Rates {
			if rate.Enabled {
				ids = append(ids, rate.ID)
			}
		}
		return travelline.NewClient(cfg.Source.BaseURL, nil, ids)
	default:
		return nil, fmt.Errorf("unsupported source type")
	}
}

// SourceClientKey разделяет настройки, определяющие поведение клиента.
// Bnovo не хранит тарифную политику, его ключ остаётся прежним.
func SourceClientKey(cfg config.Config, origin string) string {
	key := cfg.Source.Type + "/" + origin
	if cfg.Source.Type != "travelline" {
		return key
	}
	canonical, err := SourceKey(origin)
	if err == nil {
		origin = canonical
	}
	set := make(map[string]struct{})
	for _, rate := range cfg.Rates {
		if rate.Enabled {
			set[rate.ID] = struct{}{}
		}
	}
	rates := make([]string, 0, len(set))
	for id := range set {
		rates = append(rates, id)
	}
	sort.Strings(rates)
	policy, _ := json.Marshal(rates)
	hash := sha256.Sum256(policy)
	return fmt.Sprintf("%s/%s/%s/%x", cfg.Source.Type, origin, cfg.Source.PropertyID, hash)
}
