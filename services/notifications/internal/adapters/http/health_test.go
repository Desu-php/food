package httpadapter

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
)

func TestReadiness(t *testing.T) {
	for _, failure := range []bool{false, true} {
		h := NewHandler(func(context.Context) error {
			if failure {
				return errors.New("dependency failed")
			}
			return nil
		})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/ready", nil))
		expected := 200
		if failure {
			expected = 503
		}
		if w.Code != expected {
			t.Fatal(w.Code)
		}
	}
}
