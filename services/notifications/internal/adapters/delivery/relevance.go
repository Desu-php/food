package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type Relevance struct {
	BaseURL, Key string
	Client       *http.Client
}

func (r *Relevance) Live(ctx context.Context, offer, courier int64) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/internal/push/offers/%d?courier_id=%d", strings.TrimRight(r.BaseURL, "/"), offer, courier), nil)
	if err != nil {
		return false, errors.New("relevance request failed")
	}
	req.Header.Set("Authorization", "Bearer "+r.Key)
	response, err := r.Client.Do(req)
	if err != nil {
		return false, errors.New("relevance unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return false, errors.New("relevance unavailable")
	}
	var body struct {
		Live bool `json:"live"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 1024)).Decode(&body) != nil {
		return false, errors.New("invalid relevance response")
	}
	return body.Live, nil
}
