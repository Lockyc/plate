package ml

import (
	"testing"
	"time"

	"github.com/lockyc/plate/internal/engine"
)

func TestWithTimeout(t *testing.T) {
	c := engine.Cmd{Timeout: Timeout}
	WithTimeout(5 * time.Hour)(&c)
	if c.Timeout != 5*time.Hour {
		t.Errorf("timeout %v", c.Timeout)
	}
}
