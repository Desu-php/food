package security

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Authority resolves current permissions from Auth after local JWT verification.
// It deliberately has no cache: revocations must affect the next request.
type Authority struct {
	BaseURL, Key string
	Client       *http.Client
}

func NewAuthority(base, key, environment string) (*Authority, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Scheme != "https" && !(environment != "production" && u.Scheme == "http")) || len(key) < 32 {
		return nil, errors.New("trusted Auth URL and service key required; production requires HTTPS")
	}
	return &Authority{strings.TrimSuffix(base, "/"), key, &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (a *Authority) Principal(ctx context.Context, token string) (Principal, int) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.BaseURL+"/internal/v1/principal", nil)
	if err != nil {
		return Principal{}, 503
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Auth-Service-Key", a.Key)
	res, err := a.Client.Do(req)
	if err != nil {
		return Principal{}, 503
	}
	defer res.Body.Close()
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return Principal{}, res.StatusCode
	}
	if res.StatusCode != 200 {
		return Principal{}, 503
	}
	d := json.NewDecoder(io.LimitReader(res.Body, 64<<10))
	d.DisallowUnknownFields()
	var p Principal
	if d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF || p.Subject == "" || p.UserID <= 0 {
		return Principal{}, 503
	}
	return p, 200
}
func (a *Authority) DeviceAuthorized(ctx context.Context, user int64, kind string, owner int64) (bool, error) {
	body, _ := json.Marshal(struct {
		UserID    int64  `json:"user_id"`
		OwnerType string `json:"owner_type"`
		OwnerID   int64  `json:"owner_id"`
	}{user, kind, owner})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.BaseURL+"/internal/v1/device-authorized", bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("X-Auth-Service-Key", a.Key)
	req.Header.Set("Content-Type", "application/json")
	res, err := a.Client.Do(req)
	if err != nil {
		return false, errors.New("Auth authority unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return false, errors.New("Auth authority unavailable")
	}
	var v struct {
		Allowed bool `json:"allowed"`
	}
	d := json.NewDecoder(io.LimitReader(res.Body, 1024))
	if d.Decode(&v) != nil || d.Decode(new(any)) != io.EOF {
		return false, errors.New("invalid Auth authority response")
	}
	return v.Allowed, nil
}
