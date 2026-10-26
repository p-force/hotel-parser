// Package bnovo contains source-specific types and extraction logic for the
// public Bnovo booking widget. These types describe observed public offers,
// not confirmed PMS inventory or bookings.
package bnovo

import "time"

// SearchRequest describes one query to a property's public booking widget.
// The caller supplies calendar dates in the property's local time zone.
type SearchRequest struct {
	// PropertyID is the public Bnovo widget UID, not a PMS hotel ID.
	PropertyID string

	// CheckIn is the inclusive arrival date in the property's local time zone.
	CheckIn time.Time
	// CheckOut is the exclusive departure date in the property's local time zone.
	// The stay length is the number of calendar days between CheckIn and CheckOut.
	CheckOut time.Time

	// Adults is the number of adult guests requested for one accommodation.
	Adults int
	// ChildrenAges contains each accompanying child's age; empty means no children.
	ChildrenAges []int
	// Language is the public widget's requested language, e.g. "ru".
	Language string
}

// RoomCategory identifies a base room category shared by accommodation variants.
// It does not represent an individual physical room or an inventory snapshot.
type RoomCategory struct {
	// ID is the base Bnovo room type ID (data-real-room-id when provided).
	ID string
	// Name is the category's human-readable title as observed from Bnovo.
	Name string
	// AreaM2 is the category's area in square metres; nil means unknown.
	AreaM2 *float64
}

// AccommodationVariant identifies one occupancy variant of a base room category.
// Variants for two, three and four adults may share the same physical inventory.
type AccommodationVariant struct {
	// ID is the Bnovo variant room type ID (data-room-id).
	ID string
	// CategoryID refers to RoomCategory.ID (data-real-room-id).
	CategoryID string
	// Adults is the occupancy represented by this variant, not the category's
	// maximum possible capacity. Zero means the occupancy was not determined.
	Adults int
}

// RateOffer is one observed offer for the dates/guests in its SearchResult.
// Multiple rates and accommodation variants may share the same physical rooms.
type RateOffer struct {
	// CategoryID refers to the base physical RoomCategory.ID.
	CategoryID string
	// VariantID refers to AccommodationVariant.ID.
	VariantID string
	// RateID is the source tariff/plan identifier.
	RateID string
	// RateName is the source tariff title.
	RateName string

	// Price is the exact decimal amount for the ENTIRE stay, not per night.
	// Format: digits with optional dot decimal separator (e.g. "4910.50").
	// Empty means unavailable or unparseable; "0" is an actual zero amount.
	Price string
	// Currency is the ISO 4217 currency code, e.g. "RUB"; empty if unknown.
	Currency string

	// MealPlan preserves the observed meal/board description; empty if unknown.
	MealPlan string
	// Cancellation preserves the observed cancellation terms without inventing
	// a refundability classification; empty if unknown.
	Cancellation string
	// Prepayment preserves the observed payment/prepayment terms; empty if unknown.
	Prepayment string

	// Visible records whether this offer row was presented in the HTML.
	// Hidden offers are not automatically considered publicly bookable.
	Visible bool
	// Availability is raw public UI evidence for this specific rate/variant.
	Availability AvailabilityEvidence
}

// AvailabilityEvidence keeps raw public widget quantity signals independent
// of any estimate of physical availability or occupied/confirmed rooms.
type AvailabilityEvidence struct {
	// DataAvailable is the integer data-available attribute; nil means absent
	// or invalid, not zero available rooms.
	DataAvailable *int
	// DataAvailableForGuests is the literal data-available-for-guests value.
	// Its semantics are unverified; nil means the attribute was not observed.
	DataAvailableForGuests *string
	// SelectableQuantities are positive enabled <option> values from the
	// corresponding quantity selector. Never sum across tariffs/variants.
	SelectableQuantities []int
	// SelectDisabled indicates that the quantity selector was disabled.
	SelectDisabled bool
}

// SearchStatus describes the outcome of interpreting one public-widget query.
type SearchStatus string

const (
	// SearchStatusSuccess means the response was parsed without material issues.
	SearchStatusSuccess SearchStatus = "success"
	// SearchStatusPartial means some offers were parsed, but others were lost or
	// material warnings occurred. Valid offers must remain available.
	SearchStatusPartial SearchStatus = "partial"
	// SearchStatusNoOffers means the response was parsed but had no matching
	// offers; it does NOT prove that all physical rooms were sold.
	SearchStatusNoOffers SearchStatus = "no_offers"
	// SearchStatusFailed means the request or its interpretation failed.
	SearchStatusFailed SearchStatus = "failed"
)

// CollectionError is a machine-readable failure that can later be persisted
// with an attempt record; codes may be extended as new source failures emerge.
type CollectionError struct {
	// Code is a stable internal error key, e.g. "timeout" or "http_error".
	Code string
	// Message is a short, sanitized diagnostic without cookies or secrets.
	Message string
	// HTTPStatus is the HTTP response status, or zero if no response arrived.
	HTTPStatus int
	// Retryable indicates whether retrying this failure may be appropriate.
	Retryable bool
}

// ParseWarning records a nonfatal issue with an incomplete/ambiguous field or
// offer. Warnings are retained even when some offers parse successfully.
type ParseWarning struct {
	// Code is a stable warning key, e.g. "price_missing".
	Code string
	// Message is a short, sanitized explanation of what was not parsed.
	Message string
	// Context identifies the affected field, category, variant or rate without
	// including raw HTML, personal data or credentials.
	Context string
}

// SearchResult describes ONE search request and retains diagnostics even if
// no offers were produced. A later persistence layer may store every attempt.
type SearchResult struct {
	// Request contains the exact query parameters used for this attempt.
	Request SearchRequest
	// CollectedAt is when the attempt started, in UTC.
	CollectedAt time.Time
	// Status distinguishes success, partial parsing, no offers and failure.
	Status SearchStatus

	// Categories contains the base room categories observed in the response.
	Categories []RoomCategory
	// Variants contains occupancy variants linked to base categories.
	Variants []AccommodationVariant
	// Offers contains parsed tariff offers for this query; may be empty.
	Offers []RateOffer

	// HTTPStatus is the HTTP response code, or zero if no response arrived.
	HTTPStatus int
	// Error holds fatal failure details; nil unless an attempt failed.
	Error *CollectionError
	// Warnings holds nonfatal extraction issues, including partial parses.
	Warnings []ParseWarning
}
