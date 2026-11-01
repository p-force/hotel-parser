// Package source задаёт общий контракт адаптеров публичных источников.
// Алиасы сохраняют совместимость существующего Bnovo API без миграции моделей.
package source

import (
	"context"

	"github.com/p-force/hotel-parser/internal/source/bnovo"
)

type SearchRequest = bnovo.SearchRequest
type SearchResult = bnovo.SearchResult
type RoomCategory = bnovo.RoomCategory
type AccommodationVariant = bnovo.AccommodationVariant
type RateOffer = bnovo.RateOffer
type AvailabilityEvidence = bnovo.AvailabilityEvidence
type ParseWarning = bnovo.ParseWarning
type CategoryAvailability = bnovo.CategoryAvailability
type CollectionError = bnovo.CollectionError

const (
	Success  = bnovo.SearchStatusSuccess
	Partial  = bnovo.SearchStatusPartial
	NoOffers = bnovo.SearchStatusNoOffers
	Failed   = bnovo.SearchStatusFailed
)

type Client interface {
	Collect(context.Context, SearchRequest) SearchResult
	SourceOrigin() string
	SourceType() string
}

type StockStatus string

const (
	StockKnown    StockStatus = "known"
	StockUnknown  StockStatus = "unknown"
	StockConflict StockStatus = "conflict"
)

type CategoryStock struct {
	CategoryID string
	Available  *int
	Status     StockStatus
	Warnings   []ParseWarning
}

func OriginKey(raw string) (string, error) { return bnovo.SourceOriginKey(raw) }
