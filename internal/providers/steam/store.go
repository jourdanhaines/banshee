package steam

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// storeTimeout bounds the store search HTTP round trip. The aggregator joins
// every provider before the list paints, so this is an upper bound on how
// much a "steam <query>" keystroke can delay the whole launcher; the query
// ctx (cancelled by the next keystroke) cuts it shorter in practice.
const storeTimeout = 1500 * time.Millisecond

// defaultStoreBase is the Steam storefront origin. The search API deliberately
// omits the cc country parameter so Steam derives the storefront from the
// caller's IP and prices come back in the user's actual currency; l=english
// keeps names stable.
const defaultStoreBase = "https://store.steampowered.com"

// storePrice is the price block of one store search item. Amounts are in
// cents of Currency.
type storePrice struct {
	Currency string `json:"currency"`
	Final    int    `json:"final"`
}

// storeItem is one result from the storesearch API. Unknown fields are
// ignored, as everywhere.
type storeItem struct {
	Type  string      `json:"type"`
	Name  string      `json:"name"`
	ID    int         `json:"id"`
	Price *storePrice `json:"price"`
}

type storeResponse struct {
	Items []storeItem `json:"items"`
}

// storeSearch queries the Steam store for term and returns the app-typed
// items in API order (the API's own relevance ranking). Bounded by the
// client timeout and ctx.
func (p *Provider) storeSearch(ctx context.Context, term string) ([]storeItem, error) {
	q := url.Values{}
	q.Set("term", term)
	q.Set("l", "english")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.storeBase+"/api/storesearch/?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "banshee")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("store search: status %d", resp.StatusCode)
	}
	var body storeResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("store search: %w", err)
	}
	apps := body.Items[:0]
	for _, it := range body.Items {
		if it.Type == "app" && it.Name != "" && it.ID > 0 {
			apps = append(apps, it)
		}
	}
	return apps, nil
}

// formatPrice renders a store item's price for the row subtitle. A missing
// price block or a zero amount is a free title.
func formatPrice(pr *storePrice) string {
	if pr == nil || pr.Final == 0 {
		return "Free"
	}
	whole, cents := pr.Final/100, pr.Final%100
	if pr.Currency == "USD" {
		return fmt.Sprintf("$%d.%02d", whole, cents)
	}
	return fmt.Sprintf("%d.%02d %s", whole, cents, pr.Currency)
}
