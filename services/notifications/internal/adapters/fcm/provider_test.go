package fcm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	firebase "firebase.google.com/go/v4"
	"github.com/Desu-php/food/services/notifications/internal/domain"
	"google.golang.org/api/option"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (fn roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }
func TestOfficialSDKAddressingAndErrors(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		status     int
		code       string
		invalid    bool
	}{
		{"accepted", `{"name":"projects/test/messages/123"}`, 200, "accepted", false},
		{"unregistered", `{"error":{"code":404,"status":"NOT_FOUND","message":"private-installation","details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"UNREGISTERED"}]}}`, 404, "unregistered", true},
		{"payload_invalid", `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"private-installation","details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"INVALID_ARGUMENT"}]}}`, 400, "invalid_argument", false},
		{"sender_mismatch", `{"error":{"code":403,"status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"SENDER_ID_MISMATCH"}]}}`, 403, "sender_mismatch", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				var message map[string]json.RawMessage
				if err := json.Unmarshal(body["message"], &message); err != nil {
					t.Fatal(err)
				}
				if string(message["fid"]) != `"private-installation"` || message["token"] != nil {
					t.Fatal("SDK addressing changed")
				}
				if message["android"] == nil || message["apns"] == nil || message["webpush"] == nil {
					t.Fatal("TTL missing")
				}
				return &http.Response{StatusCode: tt.status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(tt.body)), Request: r}, nil
			})}
			app, err := firebase.NewApp(context.Background(), &firebase.Config{ProjectID: "test"}, option.WithHTTPClient(client))
			if err != nil {
				t.Fatal(err)
			}
			sender, err := app.Messaging(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			result := (&Provider{sender}).Send(context.Background(), "private-installation", domain.PushMessage{Title: "Title", Body: "Body", ExpiresAt: time.Now().Add(time.Minute), CollapseKey: "notification-1"})
			if result.Code != tt.code || result.InvalidDevice != tt.invalid {
				t.Fatal(result)
			}
		})
	}
}
func TestExpiredMessageNeverSent(t *testing.T) {
	p := &Provider{}
	r := p.Send(context.Background(), "private-installation", domain.PushMessage{ExpiresAt: time.Now().Add(-time.Second)})
	if r.Code != "cancelled" {
		t.Fatal(r)
	}
}
func TestRealFCMOptIn(t *testing.T) {
	if os.Getenv("TEST_REAL_FCM") != "true" {
		t.Skip("manual opt-in: TEST_REAL_FCM=true, FIREBASE_PROJECT_ID, TEST_FCM_INSTALLATION_ID and ADC")
	}
	project, target := os.Getenv("FIREBASE_PROJECT_ID"), os.Getenv("TEST_FCM_INSTALLATION_ID")
	if project == "" || target == "" {
		t.Fatal("explicit project and test installation required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	p, err := New(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	result := p.Send(ctx, target, domain.PushMessage{Title: "Тест", Body: "Проверка тестового устройства", ExpiresAt: time.Now().Add(time.Minute), CollapseKey: "manual-test"})
	if result.Code != "accepted" {
		t.Fatal("FCM result", result.Code)
	}
}
func TestRetryAfter(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	if d := retryAfter("120", now); d != 2*time.Minute {
		t.Fatal(d)
	}
	if d := retryAfter(now.Add(3*time.Minute).Format(http.TimeFormat), now); d != 3*time.Minute {
		t.Fatal(d)
	}
	for _, raw := range []string{"-1", "bad", "0"} {
		if d := retryAfter(raw, now); d != 0 {
			t.Fatal(raw, d)
		}
	}
}
func TestTransientClassification(t *testing.T) {
	for _, err := range []error{context.DeadlineExceeded, context.Canceled} {
		result := classify(err)
		if !result.Temporary || result.InvalidDevice || result.Code != "unavailable" {
			t.Fatal(result)
		}
	}
}
