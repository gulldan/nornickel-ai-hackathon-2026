package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/example/auth/internal/domain"
)

// TestTooManyAttemptsError checks the message includes the retry window and
// that the error unwraps to the sentinel for errors.Is matching.
func TestTooManyAttemptsError(t *testing.T) {
	err := &domain.TooManyAttemptsError{RetryAfter: 90 * time.Second}

	if !strings.Contains(err.Error(), "1m30s") {
		t.Fatalf("Error() = %q, want it to mention the retry window", err.Error())
	}
	if !errors.Is(err, domain.ErrTooManyAttempts) {
		t.Fatalf("error must unwrap to ErrTooManyAttempts")
	}

	var tma *domain.TooManyAttemptsError
	if !errors.As(err, &tma) || tma.RetryAfter != 90*time.Second {
		t.Fatalf("errors.As did not recover the retry window, got %+v", tma)
	}
}
