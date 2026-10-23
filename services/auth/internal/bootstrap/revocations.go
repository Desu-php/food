package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Desu-php/food/services/auth/internal/adapters/postgres"
	"github.com/Desu-php/food/services/auth/internal/application"
)

func runRevocations(ctx context.Context, repo *postgres.Repository, target, key string, logger *slog.Logger) {
	client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			op, cancel := context.WithTimeout(ctx, 5*time.Second)
			v, ok, err := repo.ClaimRevocation(op, application.Secret())
			cancel()
			if err != nil || !ok {
				continue
			}
			body, _ := json.Marshal(struct {
				UserID   int64     `json:"user_id"`
				NotAfter time.Time `json:"not_after"`
			}{v.UserID, v.NotAfter})
			op, cancel = context.WithTimeout(ctx, 5*time.Second)
			req, err := http.NewRequestWithContext(op, "POST", strings.TrimSuffix(target, "/")+"/internal/v1/devices/revoke", bytes.NewReader(body))
			success := false
			if err == nil {
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-Auth-Service-Key", key)
				res, e := client.Do(req)
				if e == nil {
					success = res.StatusCode == 204
					res.Body.Close()
				}
			}
			cancel()
			op, cancel = context.WithTimeout(ctx, 5*time.Second)
			err = repo.FinishRevocation(op, v, success)
			cancel()
			if err != nil || !success {
				logger.Warn("device revocation pending", "attempt", v.Attempts)
			}
		}
	}
}
