package main

import (
	"math/rand"
	"testing"
	"time"
)

func TestReconnectPolicyFirstFailureUsesInitialDelay(t *testing.T) {
	t.Parallel()

	policy := newReconnectPolicy(rand.New(rand.NewSource(1)))
	attempt := policy.Next()

	if attempt.Number != 1 {
		t.Fatalf("expected first attempt number to be 1, got %d", attempt.Number)
	}
	if attempt.BaseDelay != reconnectInitialDelay {
		t.Fatalf("expected first base delay to be %s, got %s", reconnectInitialDelay, attempt.BaseDelay)
	}

	minDelay := time.Duration(float64(reconnectInitialDelay) * (1 - reconnectJitterRatio))
	maxDelay := time.Duration(float64(reconnectInitialDelay) * (1 + reconnectJitterRatio))
	if attempt.Delay < minDelay || attempt.Delay > maxDelay {
		t.Fatalf("expected jittered delay between %s and %s, got %s", minDelay, maxDelay, attempt.Delay)
	}
}

func TestReconnectPolicyBacksOffAndCaps(t *testing.T) {
	t.Parallel()

	policy := newReconnectPolicy(rand.New(rand.NewSource(2)))
	expected := []time.Duration{
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		30 * time.Second,
		30 * time.Second,
	}

	for i, want := range expected {
		attempt := policy.Next()
		if attempt.BaseDelay != want {
			t.Fatalf("attempt %d: expected base delay %s, got %s", i+1, want, attempt.BaseDelay)
		}
	}
}

func TestReconnectPolicyJitterStaysWithinBand(t *testing.T) {
	t.Parallel()

	policy := newReconnectPolicy(rand.New(rand.NewSource(3)))

	for i := 0; i < 8; i++ {
		attempt := policy.Next()
		minDelay := time.Duration(float64(attempt.BaseDelay) * (1 - reconnectJitterRatio))
		maxDelay := time.Duration(float64(attempt.BaseDelay) * (1 + reconnectJitterRatio))

		if attempt.Delay < minDelay || attempt.Delay > maxDelay {
			t.Fatalf("attempt %d: expected jittered delay between %s and %s, got %s", attempt.Number, minDelay, maxDelay, attempt.Delay)
		}
	}
}

func TestReconnectPolicyResetRestartsBackoff(t *testing.T) {
	t.Parallel()

	policy := newReconnectPolicy(rand.New(rand.NewSource(4)))
	_ = policy.Next()
	_ = policy.Next()
	policy.Reset()

	attempt := policy.Next()
	if attempt.Number != 1 {
		t.Fatalf("expected reset attempt number to be 1, got %d", attempt.Number)
	}
	if attempt.BaseDelay != reconnectInitialDelay {
		t.Fatalf("expected reset base delay to be %s, got %s", reconnectInitialDelay, attempt.BaseDelay)
	}
}
