package main

import (
	"errors"
	"math/rand"
	"time"

	"github.com/tarik02/proxyhub/proxynode"
)

const (
	reconnectInitialDelay = time.Second
	reconnectMaxDelay     = 30 * time.Second
	reconnectJitterRatio  = 0.2
)

type reconnectAttempt struct {
	Number    int
	BaseDelay time.Duration
	Delay     time.Duration
}

type reconnectPolicy struct {
	rng      *rand.Rand
	failures int
	nextBase time.Duration
}

func newReconnectPolicy(rng *rand.Rand) *reconnectPolicy {
	if rng == nil {
		rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}

	return &reconnectPolicy{
		rng:      rng,
		nextBase: reconnectInitialDelay,
	}
}

func (p *reconnectPolicy) Next() reconnectAttempt {
	baseDelay := p.nextBase
	p.failures++

	jitter := (p.rng.Float64()*2 - 1) * reconnectJitterRatio
	delay := time.Duration(float64(baseDelay) * (1 + jitter))

	nextBase := p.nextBase * 2
	if nextBase > reconnectMaxDelay {
		nextBase = reconnectMaxDelay
	}
	p.nextBase = nextBase

	return reconnectAttempt{
		Number:    p.failures,
		BaseDelay: baseDelay,
		Delay:     delay,
	}
}

func (p *reconnectPolicy) Reset() {
	p.failures = 0
	p.nextBase = reconnectInitialDelay
}

func classifyReconnectError(err error) string {
	switch {
	case errors.Is(err, proxynode.ErrDialFailed):
		return "dial_failure"
	case errors.Is(err, proxynode.ErrServerDisconnect):
		return "server_disconnect"
	case errors.Is(err, proxynode.ErrYamuxServerFailed), errors.Is(err, proxynode.ErrAcceptStreamFailed):
		return "session_close"
	default:
		return "app_error"
	}
}
