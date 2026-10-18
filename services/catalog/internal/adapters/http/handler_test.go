package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Desu-php/food/platform/security"
	"github.com/Desu-php/food/services/catalog/internal/application"
	"github.com/Desu-php/food/services/catalog/internal/domain"
)

// This fake is only used in tests; production always uses PostgreSQL.
type testRepository struct {
	mu      sync.Mutex
	rows    []domain.Restaurant
	nextID  int64
	failure error
}

func (s *testRepository) List(ctx context.Context, p application.Page) ([]domain.Restaurant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure != nil {
		return nil, s.failure
	}
	if p.Offset >= len(s.rows) {
		return []domain.Restaurant{}, nil
	}
	end := min(len(s.rows), p.Offset+p.Limit)
	return append([]domain.Restaurant{}, s.rows[p.Offset:end]...), nil
}
func (s *testRepository) Get(ctx context.Context, id int64) (domain.Restaurant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure != nil {
		return domain.Restaurant{}, s.failure
	}
	for _, r := range s.rows {
		if r.ID == id {
			return r, nil
		}
	}
	return domain.Restaurant{}, domain.ErrNotFound
}
func (s *testRepository) Create(ctx context.Context, r domain.Restaurant) (domain.Restaurant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure != nil {
		return domain.Restaurant{}, s.failure
	}
	r.ID = s.nextID
	s.nextID++
	r.CreatedAt = time.Now().UTC()
	r.UpdatedAt = r.CreatedAt
	s.rows = append(s.rows, r)
	return r, nil
}
func (s *testRepository) Update(ctx context.Context, id int64, changes domain.RestaurantChanges) (domain.Restaurant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure != nil {
		return domain.Restaurant{}, s.failure
	}
	for i := range s.rows {
		if s.rows[i].ID == id {
			if changes.Address != nil {
				s.rows[i].Address = *changes.Address
			}
			if changes.Name != nil {
				s.rows[i].Name = *changes.Name
			}
			if changes.IsOpen != nil {
				s.rows[i].IsOpen = *changes.IsOpen
			}
			s.rows[i].UpdatedAt = time.Now().UTC()
			return s.rows[i], nil
		}
	}
	return domain.Restaurant{}, domain.ErrNotFound
}

func testHandler(failure error, readyErr error) http.Handler {
	now := time.Now().UTC()
	repo := &testRepository{rows: []domain.Restaurant{
		{ID: 1, Name: "Pizza House", IsOpen: true, CreatedAt: now, UpdatedAt: now},
		{ID: 2, Name: "Burger Corner", IsOpen: false, CreatedAt: now, UpdatedAt: now},
	}, nextID: 3, failure: failure}
	return authenticatedCatalog(NewHandler(application.NewRestaurants(repo), nil, func(context.Context) error { return readyErr }, slog.New(slog.NewJSONHandler(io.Discard, nil)), time.Second))
}

func request(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if method == http.MethodPost || method == http.MethodPatch {
		req.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	h.ServeHTTP(response, req)
	return response
}

func checkJSON(t *testing.T, response *httptest.ResponseRecorder, status int) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, status, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if !json.Valid(response.Body.Bytes()) {
		t.Fatalf("invalid response JSON: %s", response.Body.String())
	}
}

func TestHealthAndReadiness(t *testing.T) {
	for _, path := range []string{"/health", "/ready"} {
		response := request(testHandler(nil, nil), http.MethodGet, path, "")
		checkJSON(t, response, http.StatusOK)
		if strings.TrimSpace(response.Body.String()) != `{"status":"ok"}` {
			t.Fatal(response.Body.String())
		}
	}
	h := testHandler(errors.New("database offline"), errors.New("database offline"))
	checkJSON(t, request(h, http.MethodGet, "/health", ""), http.StatusOK)
	checkJSON(t, request(h, http.MethodGet, "/ready", ""), http.StatusServiceUnavailable)
}

func TestRestaurantLifecycle(t *testing.T) {
	h := testHandler(nil, nil)
	response := request(h, http.MethodPost, "/restaurants", `{"name":" New Cafe ","is_open":true}`)
	checkJSON(t, response, http.StatusCreated)
	if response.Header().Get("Location") != "/restaurants/3" {
		t.Fatal("missing location")
	}
	var created restaurantDTO
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID != 3 || created.Name != "New Cafe" || !created.IsOpen || created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatalf("created: %+v", created)
	}
	response = request(h, http.MethodGet, "/restaurants?limit=1&offset=2", "")
	checkJSON(t, response, http.StatusOK)
	var list []restaurantDTO
	if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0] != created {
		t.Fatalf("list: %+v", list)
	}
	response = request(h, http.MethodGet, "/restaurants/3", "")
	checkJSON(t, response, http.StatusOK)
	var got restaurantDTO
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got != created {
		t.Fatalf("get: %+v", got)
	}
	response = request(h, http.MethodPatch, "/restaurants/3", `{"is_open":false}`)
	checkJSON(t, response, http.StatusOK)
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.IsOpen || got.Name != created.Name || !got.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("patch false: %+v", got)
	}
	response = request(h, http.MethodPatch, "/restaurants/3", `{"name":" Renamed "}`)
	checkJSON(t, response, http.StatusOK)
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "Renamed" || got.IsOpen {
		t.Fatalf("patch name: %+v", got)
	}
	response = request(h, http.MethodGet, "/restaurants/3", "")
	var persisted restaurantDTO
	if err := json.Unmarshal(response.Body.Bytes(), &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted != got {
		t.Fatal("update not persisted")
	}
	response = request(h, http.MethodGet, "/restaurants?offset=100", "")
	checkJSON(t, response, http.StatusOK)
	if strings.TrimSpace(response.Body.String()) != "[]" {
		t.Fatal("empty list must be []")
	}
}

func TestHTTPValidationAndErrors(t *testing.T) {
	tests := []struct {
		name, method, path, body string
		status                   int
	}{
		{"missing", "GET", "/restaurants/999", "", 404},
		{"bad id", "GET", "/restaurants/abc", "", 400},
		{"zero id", "GET", "/restaurants/0", "", 400},
		{"negative id", "GET", "/restaurants/-1", "", 400},
		{"limit", "GET", "/restaurants?limit=101", "", 400},
		{"zero limit", "GET", "/restaurants?limit=0", "", 400},
		{"offset", "GET", "/restaurants?offset=-1", "", 400},
		{"bad page", "GET", "/restaurants?limit=abc", "", 400},
		{"duplicate page", "GET", "/restaurants?limit=1&limit=2", "", 400},
		{"empty page", "GET", "/restaurants?limit=", "", 400},
		{"empty name", "POST", "/restaurants", `{"name":""}`, 400},
		{"spaces", "POST", "/restaurants", `{"name":"   "}`, 400},
		{"missing name", "POST", "/restaurants", `{"is_open":true}`, 400},
		{"long name", "POST", "/restaurants", `{"name":"` + strings.Repeat("я", 201) + `"}`, 400},
		{"malformed", "POST", "/restaurants", `{"name":`, 400},
		{"empty body", "POST", "/restaurants", "", 400},
		{"multiple objects", "POST", "/restaurants", `{"name":"Cafe"} {}`, 400},
		{"trailing garbage", "POST", "/restaurants", `{"name":"Cafe"}x`, 400},
		{"wrong name type", "POST", "/restaurants", `{"name":4}`, 400},
		{"wrong bool type", "POST", "/restaurants", `{"name":"Cafe","is_open":"true"}`, 400},
		{"null bool", "POST", "/restaurants", `{"name":"Cafe","is_open":null}`, 400},
		{"server id", "POST", "/restaurants", `{"id":99,"name":"Cafe"}`, 400},
		{"server timestamp", "PATCH", "/restaurants/1", `{"updated_at":"2026-01-01"}`, 400},
		{"empty patch", "PATCH", "/restaurants/1", `{}`, 400},
		{"null patch", "PATCH", "/restaurants/1", `{"name":null,"is_open":true}`, 400},
		{"invalid patch name", "PATCH", "/restaurants/1", `{"name":" "}`, 400},
		{"missing patch target", "PATCH", "/restaurants/999", `{"name":"Cafe"}`, 404},
		{"patch id", "PATCH", "/restaurants/abc", `{"name":"Cafe"}`, 400},
		{"unknown endpoint", "GET", "/unknown", "", 404},
		{"wrong method", "DELETE", "/restaurants/1", "", 405},
		{"large body", "POST", "/restaurants", `{"name":"` + strings.Repeat("a", maxBodyBytes) + `"}`, 413},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := testHandler(nil, nil)
			response := request(h, tt.method, tt.path, tt.body)
			checkJSON(t, response, tt.status)
			var body map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body["error"] == "" {
				t.Fatalf("expected JSON error: %s", response.Body.String())
			}
			// Failed mutations must not create additional rows.
			response = request(h, http.MethodGet, "/restaurants", "")
			var rows []restaurantDTO
			if err := json.Unmarshal(response.Body.Bytes(), &rows); err != nil || len(rows) != 2 {
				t.Fatal("failed request changed catalog")
			}
		})
	}
	req := httptest.NewRequest(http.MethodPost, "/restaurants", strings.NewReader(`{"name":"Cafe"}`))
	req.Header.Set("Content-Type", "text/plain")
	response := httptest.NewRecorder()
	testHandler(nil, nil).ServeHTTP(response, req)
	checkJSON(t, response, http.StatusUnsupportedMediaType)
}

func TestInternalErrorsAreHidden(t *testing.T) {
	for _, tt := range []struct {
		err    error
		status int
	}{
		{errors.New("secret SQL and database password"), 500},
		{context.DeadlineExceeded, 504},
		{context.Canceled, 408},
	} {
		response := request(testHandler(tt.err, nil), http.MethodGet, "/restaurants/1", "")
		checkJSON(t, response, tt.status)
		if strings.Contains(response.Body.String(), "secret") {
			t.Fatal("internal error exposed")
		}
	}
}

func TestConcurrentHTTPRequests(t *testing.T) {
	h := testHandler(nil, nil)
	const count = 12
	ids := make(chan int64, count)
	var wg sync.WaitGroup
	for range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response := request(h, http.MethodPost, "/restaurants", `{"name":"Cafe","is_open":true}`)
			if response.Code != 201 {
				t.Errorf("create: %d", response.Code)
				return
			}
			var created restaurantDTO
			if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
				t.Error(err)
				return
			}
			ids <- created.ID
			for _, path := range []string{"/restaurants", fmt.Sprintf("/restaurants/%d", created.ID)} {
				if got := request(h, http.MethodGet, path, ""); got.Code != 200 {
					t.Errorf("GET %s: %d", path, got.Code)
				}
			}
		}()
	}
	wg.Wait()
	close(ids)
	seen := map[int64]bool{}
	for id := range ids {
		if seen[id] || id < 3 {
			t.Errorf("duplicate or invalid id: %d", id)
		}
		seen[id] = true
	}
	if len(seen) != count {
		t.Fatalf("created %d, want %d", len(seen), count)
	}
}

func authenticatedCatalog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(security.WithPrincipal(r.Context(), security.Principal{UserID: 1, Roles: []string{"admin"}})))
	})
}
