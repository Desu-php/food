package domain

import (
	"math"
	"testing"
	"time"
)

func TestLocationInvariants(t *testing.T) {
	now := time.Now().UTC()
	valid := Location{CourierID: 7, Latitude: 40.28, Longitude: 69.62, AccuracyMeters: 12, RecordedAt: now, ReceivedAt: now}
	for _, tc := range []struct {
		name   string
		change func(*Location)
	}{
		{"latitude", func(l *Location) { l.Latitude = 91 }},
		{"longitude", func(l *Location) { l.Longitude = -181 }},
		{"nan", func(l *Location) { l.Latitude = math.NaN() }},
		{"infinite accuracy", func(l *Location) { l.AccuracyMeters = math.Inf(1) }},
		{"poor accuracy", func(l *Location) { l.AccuracyMeters = 101 }},
		{"unknown accuracy", func(l *Location) { l.AccuracyMeters = 0 }},
		{"negative accuracy", func(l *Location) { l.AccuracyMeters = -1 }},
		{"missing time", func(l *Location) { l.RecordedAt = time.Time{} }},
		{"future time", func(l *Location) { l.RecordedAt = now.Add(6 * time.Second) }},
		{"old time", func(l *Location) { l.RecordedAt = now.Add(-time.Minute) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := valid
			tc.change(&l)
			if l.Validate(now, time.Minute, 100) == nil {
				t.Fatal("invalid observation accepted")
			}
		})
	}
	if err := valid.Validate(now, time.Minute, 100); err != nil || !valid.Fresh(now, time.Minute) {
		t.Fatal(err)
	}
	if valid.Fresh(now.Add(time.Minute), time.Minute) {
		t.Fatal("stale observation exposed")
	}
	valid.Latitude = 89
	if err := valid.Validate(now, time.Minute, 100); err != nil {
		t.Fatal("tracking must support polar coordinates", err)
	}
}
