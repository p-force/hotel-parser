package collect

import (
	"context"
	"encoding/json"
	"io"
	"math/big"
	"slices"
	"sort"

	"github.com/p-force/hotel-parser/internal/config"
	"github.com/p-force/hotel-parser/internal/source/bnovo"
	"github.com/p-force/hotel-parser/internal/storage"
)

func selectionPolicy(hotel config.Config) *storage.SelectionPolicy {
	p := &storage.SelectionPolicy{Mode: "all_visible", RateIDs: []string{}}
	if hotel.Source.Type == "travelline" {
		p.Mode = "allowlist"
		for _, r := range hotel.Rates {
			if r.Enabled {
				p.RateIDs = append(p.RateIDs, r.ID)
			}
		}
		sort.Strings(p.RateIDs)
	}
	return p
}

// AbsenceDiagnostic не заменяет stock/job status. Scope и ссылки на факты
// разделяют сбой сценария, наблюдение категории и конкретный тариф.
type AbsenceDiagnostic struct {
	JobID      string             `json:"job_id"`
	CategoryID string             `json:"category_id,omitempty"`
	RateID     string             `json:"rate_id,omitempty"`
	Scope      string             `json:"scope"`
	Code       string             `json:"code"`
	Cause      string             `json:"cause"`
	Verdict    string             `json:"verdict"` // confirmed, observation, unknown, conflict
	Evidence   DiagnosticEvidence `json:"evidence"`
}

type DiagnosticEvidence struct {
	AttemptID            string              `json:"attempt_id,omitempty"`
	RelatedJobID         string              `json:"related_job_id,omitempty"`
	RelatedAttemptID     string              `json:"related_attempt_id,omitempty"`
	VariantID            string              `json:"variant_id,omitempty"`
	ErrorCode            string              `json:"error_code,omitempty"`
	HTTPStatus           *int                `json:"http_status"`
	HasOffers            *bool               `json:"has_offers"`
	ConfirmedUnavailable *bool               `json:"confirmed_unavailable"`
	Available            *int                `json:"available"`
	StockStatus          storage.StockStatus `json:"stock_status,omitempty"`
	AttemptWarnings      []storage.Warning   `json:"attempt_warnings"`
	CategoryWarnings     []storage.Warning   `json:"category_warnings"`
}

type RunDiagnostics struct {
	Format string `json:"format"`
	// Здесь хранятся исходные source/origin/property/scenario, timestamps,
	// предложения и политика отбора: выводы можно проверить после рестарта.
	Data     storage.RunDiagnosticData `json:"data"`
	Findings []AbsenceDiagnostic       `json:"findings"`
}

// DiagnoseRun использует только сохранённые факты. Строки сообщений и неизвестные
// warning codes никогда не интерпретируются как подтверждённое бизнес-ограничение.
func DiagnoseRun(data storage.RunDiagnosticData) RunDiagnostics {
	// Credential lease не нужен для объяснения отсутствия предложения.
	// Копируем только изменяемые здесь оболочки; исходная история не меняется.
	public := data
	public.Scenarios = append([]storage.DiagnosticAttempt{}, data.Scenarios...)
	for i := range public.Scenarios {
		s := &public.Scenarios[i]
		s.Job.LeaseOwner, s.Job.LeaseToken, s.Job.LeasedUntil = "", 0, nil
		if s.Attempt != nil {
			attempt := *s.Attempt
			attempt.Lease = nil
			s.Attempt = &attempt
		}
	}
	r := RunDiagnostics{Format: "availability-diagnostics/v1", Data: public, Findings: []AbsenceDiagnostic{}}
	for _, scenario := range data.Scenarios {
		a := scenario.Attempt
		base := AbsenceDiagnostic{JobID: scenario.Job.ID, Scope: "scenario", Code: "attempt_missing", Cause: "unknown", Verdict: "unknown"}
		if a == nil {
			r.Findings = append(r.Findings, base)
			continue
		}
		base.Evidence = DiagnosticEvidence{AttemptID: a.ID, HTTPStatus: &a.HTTPStatus, AttemptWarnings: a.Warnings}
		if a.Outcome == storage.OutcomeFailed {
			base.Code = "unclassified_failure"
			if a.Error != nil {
				base.Evidence.ErrorCode = a.Error.Code
				if technicalCode(a.Error.Code) {
					base.Code, base.Cause, base.Verdict = "technical_error", "technical_error", "confirmed"
				}
			}
			r.Findings = append(r.Findings, base)
		}
		if len(a.Observations) == 0 && a.Outcome != storage.OutcomeFailed {
			base.Code = "observations_missing"
			r.Findings = append(r.Findings, base)
		}
		if data.Inventory != nil {
			for _, expected := range data.Inventory.Categories {
				found := false
				for _, o := range a.Observations {
					if o.CategoryID == expected.SourceRoomID {
						found = true
						break
					}
				}
				if !found {
					d := base
					d.Scope, d.CategoryID, d.Code, d.Cause, d.Verdict = "category", expected.SourceRoomID, "observation_missing", "unknown", "unknown"
					r.Findings = append(r.Findings, d)
				}
			}
		}
		for _, o := range a.Observations {
			usable := usableOffers(scenario.Job.Scenario, o)
			if len(usable) > 0 && !o.ConfirmedUnavailable && o.StockStatus != storage.StockConflict && o.HasOffers {
				continue
			}
			d := base
			d.Scope, d.CategoryID, d.Code, d.Cause, d.Verdict = "category", o.CategoryID, "insufficient_evidence", "unknown", "unknown"
			d.Evidence.HasOffers, d.Evidence.ConfirmedUnavailable = ptrBool(o.HasOffers), ptrBool(o.ConfirmedUnavailable)
			d.Evidence.Available, d.Evidence.StockStatus, d.Evidence.CategoryWarnings = o.Available, o.StockStatus, o.Warnings
			if o.StockStatus == storage.StockConflict || (o.HasOffers && o.ConfirmedUnavailable) || (o.ConfirmedUnavailable && o.Available != nil && *o.Available > 0) || (len(usable) > 0 && (!o.HasOffers || o.ConfirmedUnavailable)) {
				d.Code, d.Verdict = "availability_evidence_conflict", "conflict"
			} else if o.ConfirmedUnavailable {
				d.Code, d.Verdict = "explicit_unavailability", "observation"
			}
			r.Findings = append(r.Findings, d)
			if a.Outcome == storage.OutcomeFailed || len(o.Offers) != 0 || o.HasOffers || o.StockStatus == storage.StockConflict || !diagnosticWarningsSafe(a.Warnings) || !diagnosticWarningsSafe(o.Warnings) {
				continue
			}
			for _, longer := range data.Scenarios {
				if !comparableScenarios(scenario, longer) {
					continue
				}
				for _, other := range longer.Attempt.Observations {
					if other.CategoryID != o.CategoryID || !other.HasOffers || other.ConfirmedUnavailable || other.StockStatus == storage.StockConflict || !diagnosticWarningsSafe(other.Warnings) {
						continue
					}
					for _, offer := range usableOffers(longer.Job.Scenario, other) {
						if longer.Attempt.SelectionPolicy.Mode == "allowlist" && !slices.Contains(longer.Attempt.SelectionPolicy.RateIDs, offer.RateID) {
							continue
						}
						finding := d
						finding.Scope, finding.RateID = "rate", offer.RateID
						finding.Code, finding.Verdict = "duration_dependent_availability", "observation"
						finding.Evidence.RelatedJobID, finding.Evidence.RelatedAttemptID = longer.Job.ID, longer.Attempt.ID
						finding.Evidence.VariantID = offer.VariantID
						r.Findings = append(r.Findings, finding)
					}
				}
			}
		}
	}
	sort.Slice(r.Findings, func(i, j int) bool {
		a, _ := json.Marshal(r.Findings[i])
		b, _ := json.Marshal(r.Findings[j])
		return string(a) < string(b)
	})
	return r
}

func technicalCode(code string) bool {
	switch code {
	case "http_error", "timeout", "canceled", "transport_error", "parse_error", "response_too_large", "redirect_blocked", "invalid_request", "internal_error":
		return true
	}
	return false
}
func ptrBool(b bool) *bool { return &b }
func diagnosticWarningsSafe(warnings []storage.Warning) bool {
	for _, w := range warnings {
		switch w.Code {
		case "stock_evidence_missing", "stock_evidence_incomplete", "category_availability_unknown":
		default:
			return false
		}
	}
	return true
}
func usableOffers(s storage.Scenario, o storage.Observation) []storage.Offer {
	result := []storage.Offer{}
	for _, f := range o.Offers {
		if !f.Visible || f.SelectDisabled || f.Price == nil || f.Currency == nil || (f.RowAvailable != nil && !*f.RowAvailable) {
			continue
		}
		price, ok := new(big.Rat).SetString(*f.Price)
		if !ok || price.Sign() <= 0 || len(*f.Currency) != 3 || (f.DataAvailable != nil && *f.DataAvailable <= 0) || (f.ReportedQuantity != nil && *f.ReportedQuantity <= 0) {
			continue
		}
		for _, v := range o.Variants {
			if v.ID != f.VariantID {
				continue
			}
			matches := v.Adults == s.Adults
			// Переиспользуем действующую семантику Bnovo, не вводим своё >=.
			if s.Source == "bnovo" {
				matches = (bnovo.AccommodationVariant{Adults: v.Adults}).MatchesAdults(s.Adults, bnovo.AvailabilityEvidence{RowAvailable: f.RowAvailable})
			}
			if matches {
				result = append(result, f)
			}
		}
	}
	return result
}
func comparableScenarios(a, b storage.DiagnosticAttempt) bool {
	if a.Attempt == nil || b.Attempt == nil || a.Attempt.Outcome == storage.OutcomeFailed || b.Attempt.Outcome == storage.OutcomeFailed || a.Attempt.SelectionPolicy == nil || b.Attempt.SelectionPolicy == nil {
		return false
	}
	x, y := a.Job.Scenario, b.Job.Scenario
	if x.Source != "bnovo" && x.Source != "travelline" {
		return false
	}
	if a.Job.RunID != b.Job.RunID || x.LOS != 1 || (y.LOS != 2 && y.LOS != 3) || x.PropertyID != y.PropertyID || x.Source != y.Source || x.SourceOrigin != y.SourceOrigin || x.SourcePropertyID != y.SourcePropertyID || !x.CheckIn.Equal(y.CheckIn) || x.Adults != y.Adults || x.Rooms != y.Rooms || x.Language != y.Language || !slices.Equal(x.ChildrenAges, y.ChildrenAges) {
		return false
	}
	if !x.CheckOut.Equal(x.CheckIn.AddDate(0, 0, x.LOS)) || !y.CheckOut.Equal(y.CheckIn.AddDate(0, 0, y.LOS)) {
		return false
	}
	p, q := a.Attempt.SelectionPolicy, b.Attempt.SelectionPolicy
	if p.Mode != q.Mode || (p.Mode != "all_visible" && p.Mode != "allowlist") || (x.Source == "travelline" && p.Mode != "allowlist") || (x.Source == "bnovo" && p.Mode != "all_visible") {
		return false
	}
	if !slices.Equal(p.RateIDs, q.RateIDs) {
		return false
	}
	return diagnosticWarningsSafe(b.Attempt.Warnings)
}

// WriteRunDiagnostics не вызывает сбор, не меняет БД и не зависит от CSV cache.
// До завершения run возвращает storage.ErrIncomplete, ничего не записывая.
func WriteRunDiagnostics(ctx context.Context, store *storage.Store, runID string, output io.Writer) error {
	data, err := store.ReadRunDiagnostics(ctx, runID)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	report := DiagnoseRun(data)
	if err := ctx.Err(); err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}
