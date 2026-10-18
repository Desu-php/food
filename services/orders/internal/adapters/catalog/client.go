package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"example.com/food/contracts/geo"
	"example.com/food/orders/internal/application"
	"example.com/food/orders/internal/domain"
)

type Client struct {
	baseURL string
	http    *http.Client
}

var _ application.CatalogClient = (*Client)(nil)

func NewClient(baseURL string, timeout time.Duration) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || timeout <= 0 {
		return nil, fmt.Errorf("invalid catalog URL or timeout")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = timeout
	transport.MaxConnsPerHost = 20
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: timeout, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) Close() { c.http.CloseIdleConnections() }
func (c *Client) Quote(ctx context.Context, restaurantID int64, items []application.RequestedItem) (application.CatalogQuote, error) {
	body, err := json.Marshal(struct {
		RestaurantID int64                       `json:"restaurant_id"`
		Items        []application.RequestedItem `json:"items"`
	}{restaurantID, items})
	if err != nil {
		return application.CatalogQuote{}, fmt.Errorf("encode catalog quote: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/internal/v1/menu/quote", bytes.NewReader(body))
	if err != nil {
		return application.CatalogQuote{}, fmt.Errorf("build catalog quote: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return application.CatalogQuote{}, fmt.Errorf("request catalog quote: %w", application.ErrCatalogUnavailable)
	}
	defer resp.Body.Close()
	body, err = io.ReadAll(io.LimitReader(resp.Body, (256<<10)+1))
	if err != nil || len(body) > 256<<10 {
		return application.CatalogQuote{}, application.ErrCatalogUnavailable
	}
	if resp.StatusCode != http.StatusOK {
		var failure struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(body, &failure) != nil {
			return application.CatalogQuote{}, application.ErrCatalogUnavailable
		}
		for _, mapping := range []struct {
			code   string
			status int
			err    error
		}{
			{"restaurant_not_found", 404, application.ErrRestaurantNotFound},
			{"item_not_found", 404, application.ErrItemNotFound},
			{"restaurant_unavailable", 409, application.ErrRestaurantUnavailable},
			{"item_unavailable", 409, application.ErrItemUnavailable},
			{"item_restaurant_mismatch", 409, application.ErrItemRestaurantMismatch},
		} {
			if failure.Code == mapping.code && resp.StatusCode == mapping.status {
				return application.CatalogQuote{}, mapping.err
			}
		}
		return application.CatalogQuote{}, application.ErrCatalogUnavailable
	}
	var quote struct {
		RestaurantLocation *geo.Point `json:"restaurant_location"`
		PickupAddress      string     `json:"pickup_address"`
		RestaurantID       int64      `json:"restaurant_id"`
		Currency           string     `json:"currency"`
		Items              []struct {
			MenuItemID     int64  `json:"menu_item_id"`
			RestaurantID   int64  `json:"restaurant_id"`
			Name           string `json:"name"`
			Quantity       int64  `json:"quantity"`
			UnitPriceMinor int64  `json:"unit_price_minor"`
			Currency       string `json:"currency"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &quote); err != nil || quote.RestaurantID != restaurantID || quote.Currency != domain.CurrencyTJS || len(quote.Items) != len(items) {
		return application.CatalogQuote{}, application.ErrCatalogUnavailable
	}
	if quote.RestaurantLocation != nil && !quote.RestaurantLocation.Valid() {
		return application.CatalogQuote{}, application.ErrCatalogUnavailable
	}
	result := make([]domain.PricedItem, 0, len(quote.Items))
	expected := make(map[int64]int64, len(items))
	for _, p := range items {
		expected[p.MenuItemID] = p.Quantity
	}
	for _, p := range quote.Items {
		q, ok := expected[p.MenuItemID]
		if !ok || q != p.Quantity || p.RestaurantID != restaurantID || p.Currency != quote.Currency || p.UnitPriceMinor <= 0 || strings.TrimSpace(p.Name) == "" || !utf8.ValidString(p.Name) || strings.ContainsRune(p.Name, 0) || utf8.RuneCountInString(p.Name) > 200 {
			return application.CatalogQuote{}, application.ErrCatalogUnavailable
		}
		delete(expected, p.MenuItemID)
		result = append(result, domain.PricedItem{MenuItemID: p.MenuItemID, RestaurantID: p.RestaurantID, Name: p.Name, Quantity: p.Quantity, UnitPriceMinor: p.UnitPriceMinor, Currency: p.Currency})
	}
	return application.CatalogQuote{RestaurantLocation: quote.RestaurantLocation, Items: result, PickupAddress: quote.PickupAddress}, nil
}
