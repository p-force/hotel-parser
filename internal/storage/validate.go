package storage

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var codePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,127}$`)
var decimalPattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?$`)
var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

func civil(t time.Time) time.Time { d, _ := time.Parse("2006-01-02", t.Format("2006-01-02")); return d }
func stamp(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }
func optionalStamp(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := stamp(*t)
	return &v
}
func validID(value string) bool {
	return strings.TrimSpace(value) != "" && len(value) <= 256 && !strings.ContainsAny(value, "\x00\r\n") && !strings.Contains(value, "://") && !strings.ContainsAny(value, "?&#")
}

func normalizeScenario(in Scenario) (Scenario, error) {
	if !validID(in.PropertyID) || !validID(in.Source) || !validID(in.SourcePropertyID) || in.CheckIn.IsZero() || in.CheckOut.IsZero() || in.Adults < 1 || in.Rooms < 1 || in.Language == "" || len(in.Language) > 16 {
		return in, fmt.Errorf("invalid scenario")
	}
	in.CheckIn = civil(in.CheckIn)
	in.CheckOut = civil(in.CheckOut)
	if !in.CheckOut.After(in.CheckIn) || int(in.CheckOut.Sub(in.CheckIn)/(24*time.Hour)) != in.LOS {
		return in, fmt.Errorf("scenario dates and LOS disagree")
	}
	origin, err := url.Parse(in.SourceOrigin)
	if err != nil || origin.Hostname() == "" || (origin.Scheme != "https" && origin.Scheme != "http") || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || (origin.Path != "" && origin.Path != "/") {
		return in, fmt.Errorf("source origin must not contain credentials, path or query")
	}
	host := strings.TrimSuffix(strings.ToLower(origin.Hostname()), ".")
	port := origin.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return in, fmt.Errorf("invalid source port")
		}
		port = strconv.Itoa(n)
	}
	if (origin.Scheme == "https" && port == "443") || (origin.Scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	in.SourceOrigin = origin.Scheme + "://" + host
	in.ChildrenAges = append([]int{}, in.ChildrenAges...)
	for _, age := range in.ChildrenAges {
		if age < 0 || age > 17 {
			return in, fmt.Errorf("invalid child age")
		}
	}
	return in, nil
}
func digest(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("invalid storage payload")
	}
	sum := sha256.Sum256(data)
	return sum[:], nil
}
func normalizeWarnings(input []Warning) ([]Warning, error) {
	result := append([]Warning{}, input...)
	for _, w := range result {
		if !codePattern.MatchString(w.Code) || len(w.Context) > 512 || strings.Contains(w.Context, "://") || strings.ContainsAny(w.Context, "?&#\x00\r\n") {
			return nil, fmt.Errorf("invalid warning diagnostics")
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Code == result[j].Code {
			return result[i].Context < result[j].Context
		}
		return result[i].Code < result[j].Code
	})
	return result, nil
}
func normalizeAttempt(in AttemptInput) (AttemptInput, []byte, error) {
	fail := func() (AttemptInput, []byte, error) { return in, nil, fmt.Errorf("invalid attempt") }
	if in.SelectionPolicy != nil {
		p := *in.SelectionPolicy
		p.RateIDs = append([]string{}, p.RateIDs...)
		sort.Strings(p.RateIDs)
		if (p.Mode != "all_visible" && p.Mode != "allowlist") || (p.Mode == "all_visible" && len(p.RateIDs) != 0) || (p.Mode == "allowlist" && len(p.RateIDs) == 0) {
			return fail()
		}
		for i, id := range p.RateIDs {
			if !validID(id) || (i > 0 && id == p.RateIDs[i-1]) {
				return fail()
			}
		}
		in.SelectionPolicy = &p
	}
	if in.JobID == "" || in.Number < 1 || in.StartedAt.IsZero() || in.FinishedAt.Before(in.StartedAt) || in.DurationMS < 0 || (in.QueueWaitMS != nil && *in.QueueWaitMS < 0) || (in.HTTPStatus != 0 && (in.HTTPStatus < 100 || in.HTTPStatus > 599)) {
		return fail()
	}
	switch in.Outcome {
	case OutcomeSuccess, OutcomePartial, OutcomeNoOffers:
		if in.Error != nil || in.NextRetryAt != nil {
			return fail()
		}
	case OutcomeFailed:
		if in.Error == nil || !codePattern.MatchString(in.Error.Code) {
			return fail()
		}
	default:
		return fail()
	}
	if in.NextRetryAt != nil && (in.Error == nil || !in.Error.Retryable || in.NextRetryAt.Before(in.FinishedAt)) {
		return fail()
	}
	in.StartedAt = stamp(in.StartedAt)
	in.FinishedAt = stamp(in.FinishedAt)
	in.CollectedAt = optionalStamp(in.CollectedAt)
	in.NextRetryAt = optionalStamp(in.NextRetryAt)
	var err error
	in.Warnings, err = normalizeWarnings(in.Warnings)
	if err != nil {
		return in, nil, err
	}
	in.Observations = append([]Observation{}, in.Observations...)
	sort.Slice(in.Observations, func(i, j int) bool { return in.Observations[i].CategoryID < in.Observations[j].CategoryID })
	for i := range in.Observations {
		o := &in.Observations[i]
		if !validID(o.CategoryID) || (i > 0 && in.Observations[i-1].CategoryID == o.CategoryID) || len(o.CategoryName) > 1024 || (o.AreaM2 != nil && (math.IsNaN(*o.AreaM2) || math.IsInf(*o.AreaM2, 0) || *o.AreaM2 <= 0)) {
			return fail()
		}
		if o.StockStatus == StockKnown {
			if o.Available == nil || *o.Available < 0 {
				return fail()
			}
		} else if (o.StockStatus != StockUnknown && o.StockStatus != StockConflict) || o.Available != nil {
			return fail()
		}
		if in.Outcome == OutcomeFailed && o.StockStatus == StockKnown {
			return fail()
		}
		o.Warnings, err = normalizeWarnings(o.Warnings)
		if err != nil {
			return in, nil, err
		}
		o.Variants = append([]Variant{}, o.Variants...)
		sort.Slice(o.Variants, func(i, j int) bool { return o.Variants[i].ID < o.Variants[j].ID })
		for j, v := range o.Variants {
			if !validID(v.ID) || v.Adults < 0 || (j > 0 && o.Variants[j-1].ID == v.ID) {
				return fail()
			}
		}
		o.Offers = append([]Offer{}, o.Offers...)
		sort.Slice(o.Offers, func(i, j int) bool {
			if o.Offers[i].VariantID == o.Offers[j].VariantID {
				return o.Offers[i].RateID < o.Offers[j].RateID
			}
			return o.Offers[i].VariantID < o.Offers[j].VariantID
		})
		for j := range o.Offers {
			offer := &o.Offers[j]
			if (offer.ReportedQuantity != nil && *offer.ReportedQuantity < 0) || !validID(offer.VariantID) || !validID(offer.RateID) || (offer.Price != nil && !decimalPattern.MatchString(*offer.Price)) || (offer.Currency != nil && !currencyPattern.MatchString(*offer.Currency)) || (offer.DataAvailable != nil && *offer.DataAvailable < 0) {
				return fail()
			}
			if j > 0 && o.Offers[j-1].VariantID == offer.VariantID && o.Offers[j-1].RateID == offer.RateID {
				return fail()
			}
			offer.SelectableQuantities = append([]int{}, offer.SelectableQuantities...)
			for _, quantity := range offer.SelectableQuantities {
				if quantity <= 0 {
					return fail()
				}
			}
		}
	}
	hash, err := digest(in)
	return in, hash, err
}
