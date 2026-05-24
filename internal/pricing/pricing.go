// Package pricing resolves per-model token prices (from OpenRouter) and computes
// the USD cost of an LLM call. It is consumed entirely out-of-band — at trace
// projection and at execution-completion bookkeeping — never as an in-VM RPC, so
// it has no effect on deterministic replay. Prices are cached with a TTL and
// refreshed in the background; when unavailable (offline, unknown model), cost is
// simply $0.
package pricing

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Price is the per-token USD cost of a model's prompt (input) and completion
// (output) tokens.
type Price struct {
	PromptPerToken     float64
	CompletionPerToken float64
}

// Service holds a cached model→price table.
type Service struct {
	url    string
	client *http.Client

	mu        sync.RWMutex
	table     map[string]Price
	fetchedAt time.Time
	ttl       time.Duration
}

// DefaultURL is the OpenRouter models endpoint (pricing is per-token USD).
const DefaultURL = "https://openrouter.ai/api/v1/models"

// New returns a service backed by OpenRouter with a 6h cache TTL.
func New() *Service {
	return &Service{url: DefaultURL, client: &http.Client{Timeout: 15 * time.Second}, ttl: 6 * time.Hour, table: map[string]Price{}}
}

// Set replaces the price table (used by tests and for seeding).
func (s *Service) Set(table map[string]Price) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.table = table
	s.fetchedAt = time.Now()
}

// Price returns the price for a model id, matching either the full id
// (e.g. "openai/gpt-4o-mini") or the part after the vendor slash ("gpt-4o-mini").
func (s *Service) Price(model string) (Price, bool) {
	if s == nil || model == "" {
		return Price{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if p, ok := s.table[model]; ok {
		return p, true
	}
	// Try matching ignoring the vendor prefix in either direction.
	short := model
	if i := strings.LastIndex(model, "/"); i >= 0 {
		short = model[i+1:]
	}
	for id, p := range s.table {
		idShort := id
		if i := strings.LastIndex(id, "/"); i >= 0 {
			idShort = id[i+1:]
		}
		if idShort == short {
			return p, true
		}
	}
	return Price{}, false
}

// Cost returns the USD cost of inTok prompt tokens + outTok completion tokens for
// model, or 0 when the price is unknown. (cacheTok is a subset of inTok already
// counted; it is informational and not double-charged here.)
func (s *Service) Cost(model string, inTok, outTok int) float64 {
	p, ok := s.Price(model)
	if !ok {
		return 0
	}
	return float64(inTok)*p.PromptPerToken + float64(outTok)*p.CompletionPerToken
}

// Refresh fetches the latest price table from OpenRouter.
func (s *Service) Refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var body struct {
		Data []struct {
			ID      string `json:"id"`
			Pricing struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
			} `json:"pricing"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return err
	}
	table := make(map[string]Price, len(body.Data))
	for _, m := range body.Data {
		prompt, _ := strconv.ParseFloat(m.Pricing.Prompt, 64)
		completion, _ := strconv.ParseFloat(m.Pricing.Completion, 64)
		table[m.ID] = Price{PromptPerToken: prompt, CompletionPerToken: completion}
	}
	s.Set(table)
	return nil
}

// RefreshLoop refreshes once now (best-effort) and then every TTL until ctx is
// cancelled. Run it in its own goroutine.
func (s *Service) RefreshLoop(ctx context.Context) {
	_ = s.Refresh(ctx)
	t := time.NewTicker(s.ttl)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = s.Refresh(ctx)
		}
	}
}
