package collect

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type SourceLimit struct {
	Concurrency   int
	StartInterval time.Duration
}

// Limits действуют на все пакеты процесса. Sources содержит исключения по origin.
// Нулевые concurrency означают 4 на отель и 8 на источник; нулевой интервал — без пауз.
type Limits struct {
	PropertyConcurrency int
	SourceConcurrency   int
	SourceStartInterval time.Duration
	Sources             map[string]SourceLimit
}

func defaultLimits() Limits { return Limits{PropertyConcurrency: 4, SourceConcurrency: 8} }

// SourceKey нормализует origin; путь, query, userinfo и fragment никогда не входят в ключ.
func SourceKey(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "", fmt.Errorf("invalid source origin")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("invalid source scheme")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	port := u.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("invalid source port")
		}
		port = strconv.Itoa(n)
	}
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return scheme + "://" + host, nil
}

func ConfigureLimits(options Limits) error {
	if options.PropertyConcurrency == 0 {
		options.PropertyConcurrency = 4
	}
	if options.SourceConcurrency == 0 {
		options.SourceConcurrency = 8
	}
	if options.PropertyConcurrency < 1 || options.PropertyConcurrency > MaxWorkers || options.SourceConcurrency < 1 || options.SourceConcurrency > MaxWorkers || options.SourceStartInterval < 0 {
		return fmt.Errorf("invalid property/source limits")
	}
	sources := make(map[string]SourceLimit, len(options.Sources))
	for raw, limit := range options.Sources {
		key, err := SourceKey(raw)
		if err != nil {
			return err
		}
		if limit.Concurrency == 0 {
			limit.Concurrency = options.SourceConcurrency
		}
		if limit.Concurrency < 1 || limit.Concurrency > MaxWorkers || limit.StartInterval < 0 {
			return fmt.Errorf("invalid source limits")
		}
		if _, exists := sources[key]; exists {
			return fmt.Errorf("duplicate normalized source limit")
		}
		sources[key] = limit
	}
	options.Sources = sources
	globalConcurrency.Lock()
	defer globalConcurrency.Unlock()
	if globalConcurrency.batches != 0 {
		return fmt.Errorf("cannot configure limits while a batch is running")
	}
	globalConcurrency.limits = options
	globalConcurrency.properties = make(map[string]int)
	globalConcurrency.sources = make(map[string]*sourceState)
	globalConcurrency.waiters = nil
	signalLimits()
	return nil
}

type admissionWaiter struct{ ticket, property, origin string }

type sourceState struct {
	active    int
	lastStart time.Time
}
type admission struct {
	global, property, source int
	release                  func()
}

// Все три допуска резервируются атомарно. Глобальный слот не удерживается
// в ожидании property/source/rate, частично полученных ресурсов нет.
func tryAdmission(ctx context.Context, slots chan struct{}, property, origin string, ticket ...string) (admission, time.Time, <-chan struct{}, bool) {
	globalConcurrency.Lock()
	defer globalConcurrency.Unlock()
	now := time.Now()
	changed := globalConcurrency.changed
	if ctx.Err() != nil {
		return admission{}, time.Time{}, changed, false
	}
	options := globalConcurrency.limits
	limit := SourceLimit{Concurrency: options.SourceConcurrency, StartInterval: options.SourceStartInterval}
	if custom, ok := options.Sources[origin]; ok {
		limit = custom
	}
	state := globalConcurrency.sources[origin]
	if state == nil {
		state = &sourceState{}
		globalConcurrency.sources[origin] = state
	}
	currentTicket := ""
	if len(ticket) > 0 {
		currentTicket = ticket[0]
	}
	if currentTicket != "" {
		found := false
		for _, waiter := range globalConcurrency.waiters {
			found = found || waiter.ticket == currentTicket
		}
		if !found {
			globalConcurrency.waiters = append(globalConcurrency.waiters, admissionWaiter{currentTicket, property, origin})
		}
	}
	fair := true
	for _, waiter := range globalConcurrency.waiters {
		if waiter.ticket == currentTicket {
			break
		}
		if waiter.property == property || globalConcurrency.properties[waiter.property] >= options.PropertyConcurrency {
			continue
		}
		priorLimit := SourceLimit{Concurrency: options.SourceConcurrency, StartInterval: options.SourceStartInterval}
		if custom, ok := options.Sources[waiter.origin]; ok {
			priorLimit = custom
		}
		prior := globalConcurrency.sources[waiter.origin]
		if prior != nil && prior.active < priorLimit.Concurrency && (prior.lastStart.IsZero() || !now.Before(prior.lastStart.Add(priorLimit.StartInterval))) {
			fair = false
			break
		}
	}
	var rateAt time.Time
	if !state.lastStart.IsZero() && limit.StartInterval > 0 {
		rateAt = state.lastStart.Add(limit.StartInterval)
	}
	if !fair || globalConcurrency.properties[property] >= options.PropertyConcurrency || state.active >= limit.Concurrency || len(slots) >= cap(slots) || now.Before(rateAt) {
		return admission{}, rateAt, changed, false
	}
	select {
	case slots <- struct{}{}:
	default:
		return admission{}, rateAt, changed, false
	}
	if currentTicket != "" {
		for i, waiter := range globalConcurrency.waiters {
			if waiter.ticket == currentTicket {
				globalConcurrency.waiters = append(globalConcurrency.waiters[:i], globalConcurrency.waiters[i+1:]...)
				break
			}
		}
	}
	globalConcurrency.active++
	globalConcurrency.properties[property]++
	state.active++
	state.lastStart = now
	signalLimits()
	permit := admission{global: globalConcurrency.active, property: globalConcurrency.properties[property], source: state.active}
	permit.release = func() {
		globalConcurrency.Lock()
		defer globalConcurrency.Unlock()
		globalConcurrency.active--
		globalConcurrency.properties[property]--
		state.active--
		<-slots
		if globalConcurrency.properties[property] == 0 {
			delete(globalConcurrency.properties, property)
		}
		signalLimits()
	}
	return permit, time.Time{}, changed, true
}

// Вызывается только под globalConcurrency.Mutex. Ожидающие пакеты просыпаются
// при освобождении допуска и повторяют проверку без polling/busy waiting.
func signalLimits() {
	close(globalConcurrency.changed)
	globalConcurrency.changed = make(chan struct{})
}

// Снимаем ожидания пакета при прогрессе или завершении. Они не должны блокировать
// чужой источник, пока у этого пакета заняты workers или действует StartInterval.
func withdrawAdmissions(batchID string) {
	globalConcurrency.Lock()
	defer globalConcurrency.Unlock()
	changed := false
	filtered := globalConcurrency.waiters[:0]
	for _, waiter := range globalConcurrency.waiters {
		if strings.HasPrefix(waiter.ticket, batchID+"/") {
			changed = true
		} else {
			filtered = append(filtered, waiter)
		}
	}
	globalConcurrency.waiters = filtered
	if changed {
		signalLimits()
	}
}
