package fcm

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/errorutils"
	"firebase.google.com/go/v4/messaging"
	"github.com/Desu-php/food/services/notifications/internal/domain"
)

type Sender interface {
	Send(context.Context, *messaging.Message) (string, error)
}
type Provider struct{ client Sender }

func New(ctx context.Context, project string) (*Provider, error) {
	app, err := firebase.NewApp(ctx, &firebase.Config{ProjectID: project})
	if err != nil {
		return nil, errors.New("initialize Firebase ADC failed")
	}
	client, err := app.Messaging(ctx)
	if err != nil {
		return nil, errors.New("initialize Firebase messaging failed")
	}
	return &Provider{client}, nil
}
func (p *Provider) Send(ctx context.Context, installation string, m domain.PushMessage) domain.ProviderResult {
	ttl := time.Until(m.ExpiresAt)
	if ttl <= 0 {
		return domain.ProviderResult{Code: "cancelled"}
	}
	message := &messaging.Message{Fid: installation, Notification: &messaging.Notification{Title: m.Title, Body: m.Body}, Data: m.Data,
		Android: &messaging.AndroidConfig{TTL: &ttl, CollapseKey: m.CollapseKey},
		APNS:    &messaging.APNSConfig{Headers: map[string]string{"apns-expiration": strconv.FormatInt(m.ExpiresAt.Unix(), 10), "apns-collapse-id": m.CollapseKey}},
		Webpush: &messaging.WebpushConfig{Headers: map[string]string{"TTL": strconv.FormatInt(max(1, int64(ttl/time.Second)), 10)}, Notification: &messaging.WebpushNotification{Tag: m.CollapseKey}}}
	id, err := p.client.Send(ctx, message)
	if err == nil {
		return domain.ProviderResult{Code: "accepted", MessageID: id}
	}
	result := classify(err)
	if result.Temporary {
		if response := errorutils.HTTPResponse(err); response != nil {
			result.RetryAfter = max(result.RetryAfter, retryAfter(response.Header.Get("Retry-After"), time.Now()))
		}
	}
	return result
}
func classify(err error) domain.ProviderResult {
	// INVALID_ARGUMENT may concern payload, so it never revokes an installation.
	switch {
	case messaging.IsUnregistered(err):
		return domain.ProviderResult{Code: "unregistered", InvalidDevice: true}
	case messaging.IsQuotaExceeded(err):
		return domain.ProviderResult{Code: "quota_exceeded", Temporary: true, RetryAfter: time.Minute}
	case errorutils.IsUnavailable(err), errorutils.IsInternal(err), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return domain.ProviderResult{Code: "unavailable", Temporary: true, RetryAfter: time.Minute}
	case messaging.IsInvalidArgument(err):
		return domain.ProviderResult{Code: "invalid_argument"}
	case messaging.IsSenderIDMismatch(err):
		return domain.ProviderResult{Code: "sender_mismatch"}
	case messaging.IsThirdPartyAuthError(err):
		return domain.ProviderResult{Code: "platform_auth"}
	case errorutils.IsPermissionDenied(err), errorutils.IsUnauthenticated(err):
		return domain.ProviderResult{Code: "provider_auth"}
	default:
		return domain.ProviderResult{Code: "provider_unknown", Temporary: true, RetryAfter: time.Minute}
	}
}

// Fake never contacts Firebase and has no credentials. Tests may inject outcomes.
type Fake struct{}

func (Fake) Send(ctx context.Context, _ string, _ domain.PushMessage) domain.ProviderResult {
	if ctx.Err() != nil {
		return domain.ProviderResult{Code: "unavailable", Temporary: true}
	}
	return domain.ProviderResult{Code: "accepted", MessageID: "fake"}
}

func retryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds > 0 {
		return time.Duration(min(seconds, 86400)) * time.Second
	}
	if until, err := http.ParseTime(value); err == nil && until.After(now) {
		return min(until.Sub(now), 24*time.Hour)
	}
	return 0
}
