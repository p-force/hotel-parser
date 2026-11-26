// Package storage хранит историю сбора независимо от конкретного источника.
package storage

import "time"

type RunStatus string

const (
	RunRunning   RunStatus = "running"
	RunSucceeded RunStatus = "succeeded"
	RunPartial   RunStatus = "partial"
	RunFailed    RunStatus = "failed"
	RunCanceled  RunStatus = "canceled"
)

type JobStatus string

const (
	JobQueued    JobStatus = "queued"
	JobRunning   JobStatus = "running"
	JobSucceeded JobStatus = "succeeded"
	JobPartial   JobStatus = "partial"
	JobNoOffers  JobStatus = "no_offers"
	JobFailed    JobStatus = "failed"
	JobRetryable JobStatus = "retryable"
	JobCanceled  JobStatus = "canceled"
)

type Outcome string

const (
	OutcomeSuccess  Outcome = "success"
	OutcomePartial  Outcome = "partial"
	OutcomeNoOffers Outcome = "no_offers"
	OutcomeFailed   Outcome = "failed"
)

type StockStatus string

const (
	StockKnown    StockStatus = "known"
	StockUnknown  StockStatus = "unknown"
	StockConflict StockStatus = "conflict"
)

type RunInput struct {
	ID                     string // Необязательный UUID для идемпотентного создания; пустой создаёт новый запуск.
	PropertyID             string
	PeriodStart, PeriodEnd time.Time
	StartedAt              time.Time
}
type Run struct {
	RunInput
	FinishedAt *time.Time
	Status     RunStatus
}

type Scenario struct {
	PropertyID         string
	Source             string
	SourceOrigin       string // Только нормализованный origin, без credentials/path/query.
	SourcePropertyID   string
	CheckIn, CheckOut  time.Time
	LOS, Adults, Rooms int
	ChildrenAges       []int
	Language           string
}
type Job struct {
	LeaseOwner                         string
	LeaseToken                         int64
	LeasedUntil                        *time.Time
	MaxClaims, MaxAttempts             int
	ID, RunID                          string
	Scenario                           Scenario
	Status                             JobStatus
	Attempts                           int
	LastErrorCode                      string
	CreatedAt                          time.Time
	StartedAt, FinishedAt, NextRetryAt *time.Time
}

// Warning намеренно не содержит произвольный текст сообщения источника.
type Warning struct{ Code, Context string }
type AttemptError struct {
	Code      string
	Retryable bool
}

type AttemptInput struct {
	// omitempty сохраняет hash исторических payload без исходных квот.
	RoomTypeQuotas *RoomTypeQuotaSet `json:",omitempty"`
	// SelectionPolicy фиксирует применённую тарифную политику. nil у старой истории
	// не позволяет подтвердить сопоставимость отсутствующих предложений между LOS.
	SelectionPolicy       *SelectionPolicy `json:",omitempty"`
	Lease                 *Lease           `json:",omitempty"`
	JobID                 string
	Number                int
	StartedAt, FinishedAt time.Time
	DurationMS            int64
	QueueWaitMS           *int64
	CollectedAt           *time.Time
	Outcome               Outcome
	HTTPStatus            int
	Error                 *AttemptError
	Warnings              []Warning
	NextRetryAt           *time.Time // Только если следующая попытка действительно запланирована.
	Observations          []Observation
}

type SelectionPolicy struct {
	Mode    string
	RateIDs []string
}
type Attempt struct {
	ID string
	AttemptInput
}

// UnavailabilityReason — явно установленная причина недоступности категории.
type UnavailabilityReason string

const UnavailabilityReasonBookingForbidden UnavailabilityReason = "booking_forbidden"

// Observation — один публичный остаток физической категории в одной попытке.
// Тарифы и варианты — измерения; их количества не складываются.
type Observation struct {
	CategoryID, CategoryName        string
	AreaM2                          *float64
	Available                       *int
	StockStatus                     StockStatus
	HasOffers, ConfirmedUnavailable bool
	// nil сохраняет совместимость с наблюдениями без установленной причины.
	// omitempty не меняет canonical payload исторических наблюдений.
	UnavailabilityReason *UnavailabilityReason `json:",omitempty"`
	Warnings             []Warning
	Variants             []Variant
	Offers               []Offer
}
type Variant struct {
	ID     string
	Adults int
}
type Offer struct {
	// ReportedQuantity — нативная публичная квота API; nil и 0 различаются.
	// omitempty сохраняет канонический payload старых попыток с отсутствующим полем.
	ReportedQuantity            *int `json:",omitempty"`
	VariantID, RateID, RateName string
	Price                       *string // Точное десятичное число, сохраняется как NUMERIC; nil — нет данных.
	Currency                    *string
	MealPlan                    string
	Visible                     bool
	DataAvailable               *int
	RowAvailable                *bool
	DataAvailableForGuests      *string
	SelectDisabled              bool
	SelectableQuantities        []int
}

// Lease — fencing credential одного захвата. Token монотонно растёт на каждый claim.
type Lease struct {
	JobID, Owner string
	Token        int64
}

func (j Job) Lease() Lease { return Lease{JobID: j.ID, Owner: j.LeaseOwner, Token: j.LeaseToken} }
