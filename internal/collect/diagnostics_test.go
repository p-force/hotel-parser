package collect

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/p-force/hotel-parser/internal/config"
	"github.com/p-force/hotel-parser/internal/storage"
)

func diagnosticFixture(source string) storage.RunDiagnosticData {
	in := time.Date(2026, 12, 21, 0, 0, 0, 0, time.UTC)
	finish := in.Add(-time.Hour)
	data := storage.RunDiagnosticData{Run: storage.Run{RunInput: storage.RunInput{ID: "run", PropertyID: "hotel"}, Status: storage.RunSucceeded, FinishedAt: &finish}}
	for i := 1; i <= 3; i++ {
		sc := storage.Scenario{PropertyID: "hotel", Source: source, SourceOrigin: "https://example.test", SourcePropertyID: "585", CheckIn: in, CheckOut: in.AddDate(0, 0, i), LOS: i, Adults: 1, Rooms: 1, Language: "ru"}
		a := &storage.Attempt{ID: string(rune('a' + i)), AttemptInput: storage.AttemptInput{Outcome: storage.OutcomeSuccess, HTTPStatus: 200, SelectionPolicy: &storage.SelectionPolicy{Mode: "allowlist", RateIDs: []string{"rate"}}, Observations: []storage.Observation{{CategoryID: "cat", StockStatus: storage.StockUnknown}}}}
		if source == "bnovo" {
			a.SelectionPolicy = &storage.SelectionPolicy{Mode: "all_visible", RateIDs: []string{}}
		}
		if i > 1 {
			p, c := "1000.50", "RUB"
			o := &a.Observations[0]
			o.HasOffers = true
			o.Variants = []storage.Variant{{ID: "variant", Adults: 1}}
			o.Offers = []storage.Offer{{RateID: "rate", VariantID: "variant", Visible: true, Price: &p, Currency: &c}}
		}
		data.Scenarios = append(data.Scenarios, storage.DiagnosticAttempt{Job: storage.Job{ID: string(rune('1' + i)), RunID: "run", Scenario: sc}, Attempt: a})
	}
	return data
}
func diagnosticCount(r RunDiagnostics, code string) int {
	n := 0
	for _, f := range r.Findings {
		if f.Code == code {
			n++
		}
	}
	return n
}

func TestDiagnoseDurationEvidence(t *testing.T) {
	for _, source := range []string{"bnovo", "travelline"} {
		t.Run(source, func(t *testing.T) {
			data := diagnosticFixture(source)
			before, _ := json.Marshal(data)
			r := DiagnoseRun(data)
			if diagnosticCount(r, "duration_dependent_availability") != 2 || diagnosticCount(r, "insufficient_evidence") != 1 {
				t.Fatalf("findings: %+v", r.Findings)
			}
			for _, f := range r.Findings {
				if f.Code == "duration_dependent_availability" && (f.Cause != "unknown" || f.Verdict != "observation" || f.RateID != "rate" || f.Scope != "rate" || f.Evidence.RelatedAttemptID == "" || f.Evidence.AttemptID == "" || f.Evidence.VariantID != "variant") {
					t.Fatalf("lost provenance: %+v", f)
				}
			}
			after, _ := json.Marshal(data)
			if string(before) != string(after) {
				t.Fatal("input mutated")
			}
		})
	}
}

func TestDiagnoseDurationRejectsIncomparableEvidence(t *testing.T) {
	tests := []struct {
		name   string
		change func(*storage.RunDiagnosticData)
	}{
		{"legacy missing policy", func(d *storage.RunDiagnosticData) { d.Scenarios[0].Attempt.SelectionPolicy = nil }},
		{"changed allowlist", func(d *storage.RunDiagnosticData) { d.Scenarios[0].Attempt.SelectionPolicy.RateIDs = []string{"other"} }},
		{"technical failure", func(d *storage.RunDiagnosticData) {
			d.Scenarios[0].Attempt.Outcome = storage.OutcomeFailed
			d.Scenarios[0].Attempt.Error = &storage.AttemptError{Code: "timeout"}
		}},
		{"missing observation", func(d *storage.RunDiagnosticData) { d.Scenarios[0].Attempt.Observations = nil }},
		{"missing attempt", func(d *storage.RunDiagnosticData) { d.Scenarios[0].Attempt = nil }},
		{"conflict", func(d *storage.RunDiagnosticData) {
			d.Scenarios[0].Attempt.Observations[0].StockStatus = storage.StockConflict
		}},
		{"source diagnostic", func(d *storage.RunDiagnosticData) {
			d.Scenarios[0].Attempt.Warnings = []storage.Warning{{Code: "source_diagnostics", Context: "cat"}}
		}},
		{"bad dates", func(d *storage.RunDiagnosticData) {
			d.Scenarios[0].Job.Scenario.CheckOut = d.Scenarios[0].Job.Scenario.CheckIn
		}},
	}
	// Each longer scenario is changed so neither LOS can produce a false finding.
	for _, field := range []string{"hotel", "source", "origin", "public id", "date", "adults", "rooms", "children", "language", "run", "category", "hidden", "disabled", "variant", "no price", "no currency", "rate policy", "warning", "failed", "no offers"} {
		field := field
		tests = append(tests, struct {
			name   string
			change func(*storage.RunDiagnosticData)
		}{field, func(d *storage.RunDiagnosticData) {
			for i := 1; i < 3; i++ {
				s := &d.Scenarios[i]
				o := &s.Attempt.Observations[0]
				switch field {
				case "hotel":
					s.Job.Scenario.PropertyID = "other"
				case "source":
					s.Job.Scenario.Source = "other"
				case "origin":
					s.Job.Scenario.SourceOrigin = "https://other.test"
				case "public id":
					s.Job.Scenario.SourcePropertyID = "other"
				case "date":
					s.Job.Scenario.CheckIn = s.Job.Scenario.CheckIn.AddDate(0, 0, 1)
				case "adults":
					s.Job.Scenario.Adults = 2
				case "rooms":
					s.Job.Scenario.Rooms = 2
				case "children":
					s.Job.Scenario.ChildrenAges = []int{5}
				case "language":
					s.Job.Scenario.Language = "en"
				case "run":
					s.Job.RunID = "other"
				case "category":
					o.CategoryID = "other"
				case "hidden":
					o.Offers[0].Visible = false
				case "disabled":
					o.Offers[0].SelectDisabled = true
				case "variant":
					o.Variants[0].Adults = 2
				case "no price":
					o.Offers[0].Price = nil
				case "no currency":
					o.Offers[0].Currency = nil
				case "rate policy":
					o.Offers[0].RateID = "excluded"
				case "warning":
					o.Warnings = []storage.Warning{{Code: "offer_duplicate_conflict", Context: "cat/variant/rate"}}
				case "failed":
					s.Attempt.Outcome = storage.OutcomeFailed
				case "no offers":
					o.HasOffers = false
				}
			}
		}})
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := diagnosticFixture("travelline")
			tc.change(&d)
			if got := diagnosticCount(DiagnoseRun(d), "duration_dependent_availability"); got != 0 {
				t.Fatalf("false LOS evidence: %d", got)
			}
		})
	}
}

func TestDiagnoseAbsenceNeverGuessesBusinessCause(t *testing.T) {
	for _, code := range []string{"", "minimum_stay", "sales_closed", "no_rate", "sold_out", "source_diagnostics"} {
		t.Run(code, func(t *testing.T) {
			d := diagnosticFixture("travelline")
			d.Scenarios = d.Scenarios[:1]
			a := d.Scenarios[0].Attempt
			o := &a.Observations[0]
			zero := 0
			o.Available = &zero
			o.StockStatus = storage.StockKnown
			o.ConfirmedUnavailable = true
			a.Outcome = storage.OutcomeNoOffers
			if code != "" {
				a.Warnings = []storage.Warning{{Code: code, Context: "cat/rate"}}
			}
			r := DiagnoseRun(d)
			if len(r.Findings) != 1 || r.Findings[0].Cause != "unknown" || r.Findings[0].Code != "explicit_unavailability" || r.Findings[0].Evidence.Available == nil || *r.Findings[0].Evidence.Available != 0 {
				t.Fatalf("guessed cause: %+v", r)
			}
			if !reflect.DeepEqual(r.Findings[0].Evidence.AttemptWarnings, a.Warnings) {
				t.Fatal("lost warning context")
			}
		})
	}
}

func TestDiagnoseTechnicalErrorsAndConflicts(t *testing.T) {
	for _, code := range []string{"http_error", "timeout", "canceled", "transport_error", "parse_error", "response_too_large", "redirect_blocked", "invalid_request", "internal_error", "source_error", "minimum_stay"} {
		t.Run(code, func(t *testing.T) {
			d := diagnosticFixture("bnovo")
			d.Scenarios = d.Scenarios[:1]
			a := d.Scenarios[0].Attempt
			a.Outcome = storage.OutcomeFailed
			a.Error = &storage.AttemptError{Code: code}
			a.Observations = nil
			r := DiagnoseRun(d)
			expected := "unknown"
			if code != "source_error" && code != "minimum_stay" {
				expected = "confirmed"
			}
			if len(r.Findings) != 1 || r.Findings[0].Verdict != expected || r.Findings[0].Scope != "scenario" || r.Findings[0].Evidence.ErrorCode != code {
				t.Fatalf("bad diagnosis: %+v", r.Findings)
			}
		})
	}
	d := diagnosticFixture("travelline")
	d.Scenarios = d.Scenarios[1:2]
	d.Scenarios[0].Attempt.Observations[0].ConfirmedUnavailable = true
	r := DiagnoseRun(d)
	if diagnosticCount(r, "availability_evidence_conflict") != 1 || r.Findings[0].Verdict != "conflict" {
		t.Fatalf("conflict lost: %+v", r.Findings)
	}
}

func TestSelectionPolicyImmutableCopy(t *testing.T) {
	c := config.Config{Source: config.SourceConfig{Type: "travelline"}, Rates: []config.RateConfig{{ID: "b", Enabled: true}, {ID: "a", Enabled: true}, {ID: "private"}}}
	p := selectionPolicy(c)
	c.Rates[0].ID = "changed"
	if !reflect.DeepEqual(p.RateIDs, []string{"a", "b"}) || p.Mode != "allowlist" {
		t.Fatalf("policy: %+v", p)
	}
}

func TestDiagnosticNullFalseZeroAndMissingAttempt(t *testing.T) {
	d := diagnosticFixture("bnovo")
	d.Scenarios = d.Scenarios[:1]
	d.Scenarios[0].Attempt = nil
	r := DiagnoseRun(d)
	if len(r.Findings) != 1 || r.Findings[0].Code != "attempt_missing" || r.Findings[0].Evidence.HasOffers != nil || r.Findings[0].Evidence.HTTPStatus != nil {
		t.Fatal(r.Findings)
	}
	d = diagnosticFixture("bnovo")
	d.Scenarios = d.Scenarios[:1]
	r = DiagnoseRun(d)
	e := r.Findings[0].Evidence
	if e.HasOffers == nil || *e.HasOffers || e.ConfirmedUnavailable == nil || *e.ConfirmedUnavailable || e.Available != nil {
		t.Fatal("NULL and false collapsed", e)
	}
	zero := 0
	d.Scenarios[0].Attempt.Observations[0].Available = &zero
	d.Scenarios[0].Attempt.Observations[0].StockStatus = storage.StockKnown
	r = DiagnoseRun(d)
	if r.Findings[0].Evidence.Available == nil || *r.Findings[0].Evidence.Available != 0 || r.Findings[0].Cause != "unknown" {
		t.Fatal("zero is not sold out", r)
	}
}

func TestDiagnoseBnovoUsesExistingGuestSemantics(t *testing.T) {
	for _, row := range []*bool{nil, ptrBool(false), ptrBool(true)} {
		d := diagnosticFixture("bnovo")
		for i := 1; i < 3; i++ {
			d.Scenarios[i].Attempt.Observations[0].Variants[0].Adults = 2
			d.Scenarios[i].Attempt.Observations[0].Offers[0].RowAvailable = row
		}
		expected := 0
		if row != nil && *row {
			expected = 2
		}
		if n := diagnosticCount(DiagnoseRun(d), "duration_dependent_availability"); n != expected {
			t.Fatal("guest matching changed", n, expected)
		}
	}
}

func TestDiagnoseLongOfferNeedsPositivePriceAndNoExplicitZeroQuota(t *testing.T) {
	for _, kind := range []string{"price zero", "price invalid", "quantity zero"} {
		t.Run(kind, func(t *testing.T) {
			d := diagnosticFixture("travelline")
			for i := 1; i < 3; i++ {
				o := &d.Scenarios[i].Attempt.Observations[0].Offers[0]
				switch kind {
				case "price zero":
					v := "0"
					o.Price = &v
				case "price invalid":
					v := "oops"
					o.Price = &v
				case "quantity zero":
					v := 0
					o.ReportedQuantity = &v
				}
			}
			if n := diagnosticCount(DiagnoseRun(d), "duration_dependent_availability"); n != 0 {
				t.Fatal("unreliable long availability", n)
			}
		})
	}
}

func TestDiagnosticReportOmitsLeaseCredentials(t *testing.T) {
	d := diagnosticFixture("travelline")
	d.Scenarios[0].Job.LeaseOwner = "private-worker"
	d.Scenarios[0].Job.LeaseToken = 12345
	d.Scenarios[0].Attempt.Lease = &storage.Lease{Owner: "private-worker", Token: 12345, JobID: d.Scenarios[0].Job.ID}
	before, _ := json.Marshal(d)
	r := DiagnoseRun(d)
	out, _ := json.Marshal(r)
	after, _ := json.Marshal(d)
	if string(before) != string(after) {
		t.Fatal("input mutated")
	}
	if strings.Contains(string(out), "private-worker") || strings.Contains(string(out), "12345") {
		t.Fatal("lease credential exported")
	}
}
