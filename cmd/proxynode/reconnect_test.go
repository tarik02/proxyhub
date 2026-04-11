package main

import (
	"testing"
	"time"
)

type fixedJitterSource struct {
	values []float64
	index  int
}

func (f *fixedJitterSource) Float64() float64 {
	if len(f.values) == 0 {
		return 0.5
	}

	value := f.values[f.index%len(f.values)]
	f.index++
	return value
}

func TestReconnectPolicyFirstFailureUsesInitialDelay(t *testing.T) {
	t.Parallel()

	policy := newReconnectPolicy(&fixedJitterSource{values: []float64{0.5}})
	attempt := policy.Next()

	if attempt.Number != 1 {
		t.Fatalf("expected first attempt number to be 1, got %d", attempt.Number)
	}
	if attempt.BaseDelay != reconnectInitialDelay {
		t.Fatalf("expected first base delay to be %s, got %s", reconnectInitialDelay, attempt.BaseDelay)
	}
	if attempt.Delay != reconnectInitialDelay {
		t.Fatalf("expected zero-jitter delay %s, got %s", reconnectInitialDelay, attempt.Delay)
	}
}

func TestReconnectPolicyBacksOffAndCaps(t *testing.T) {
	t.Parallel()

	policy := newReconnectPolicy(&fixedJitterSource{values: []float64{0.5}})
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

	policy := newReconnectPolicy(&fixedJitterSource{values: []float64{0, 1, 0.25, 0.75}})

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

	policy := newReconnectPolicy(&fixedJitterSource{values: []float64{0.5}})
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
