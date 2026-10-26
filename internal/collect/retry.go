package collect

import (
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/p-force/hotel-parser/internal/source/bnovo"
)

// RetryPolicy.MaxAttempts включает исходную попытку; 1 отключает повторы.
// Для CollectProperties по умолчанию 3; для прежних пакетных API — 1.
type RetryPolicy struct {
	MaxAttempts                int
	InitialBackoff, MaxBackoff time.Duration
}

func (p RetryPolicy) normalized() (RetryPolicy, error) {
	if p.MaxAttempts == 0 {
		p.MaxAttempts = 1
	}
	if p.InitialBackoff == 0 {
		p.InitialBackoff = 100 * time.Millisecond
	}
	if p.MaxBackoff == 0 {
		p.MaxBackoff = 2 * time.Second
	}
	if p.MaxAttempts < 1 || p.MaxAttempts > 5 || p.InitialBackoff < 0 || p.MaxBackoff < p.InitialBackoff || p.MaxBackoff > time.Minute {
		return p, fmt.Errorf("invalid retry policy")
	}
	return p, nil
}
func retryable(result bnovo.SearchResult) bool {
	if result.Status != bnovo.SearchStatusFailed || result.Error == nil || !result.Error.Retryable {
		return false
	}
	switch result.Error.Code {
	case "timeout", "transport_error":
		return true
	case "http_error":
		switch result.HTTPStatus {
		case 429, 500, 502, 503, 504:
			return true
		}
	}
	return false
}
func retryDelay(policy RetryPolicy, attempt int, after time.Duration) time.Duration {
	ceiling := policy.InitialBackoff
	for i := 1; i < attempt && ceiling < policy.MaxBackoff; i++ {
		if ceiling > policy.MaxBackoff/2 {
			ceiling = policy.MaxBackoff
		} else {
			ceiling *= 2
		}
	}
	// Equal jitter: [ceiling/2, ceiling), с верхним пределом backoff.
	delay := ceiling / 2
	if ceiling-delay > 0 {
		delay += time.Duration(rand.Int64N(int64(ceiling - delay)))
	}
	if after > delay {
		delay = after
	} // Retry-After — минимальная задержка, не урезается MaxBackoff.
	return delay
}
