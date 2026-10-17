package bootstrap

import (
	"testing"
	"time"
)

func TestSagaConfiguration(t *testing.T) {
	t.Setenv("SAGA_ASSIGNMENT_TIMEOUT", "15s")
	t.Setenv("SAGA_CANCEL_BEFORE_PREPARING", "true")
	c, err := LoadSagaConfig()
	if err != nil || c.AssignmentTimeout != 15*time.Second || !c.CancelBeforePreparing {
		t.Fatal(c, err)
	}
	for key, value := range map[string]string{"SAGA_CREATE_TIMEOUT": "0s", "SAGA_PROGRESS_TIMEOUT": "bad", "SAGA_MAX_ATTEMPTS": "0", "SAGA_CANCEL_BEFORE_PREPARING": "bad"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, value)
			if _, err := LoadSagaConfig(); err == nil {
				t.Fatal("invalid saga config accepted")
			}
		})
	}
}
