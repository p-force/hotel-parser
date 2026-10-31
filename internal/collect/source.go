package collect

import (
	"fmt"

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

// SourceClientKey отделяет справочники публичных тарифов разных TravelLine-отелей.
func SourceClientKey(cfg config.Config, origin string) string {
	key := cfg.Source.Type + "/" + origin
	if cfg.Source.Type == "travelline" {
		key += "/" + cfg.ID
	}
	return key
}
