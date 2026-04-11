package main

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
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

type jitterSource interface {
	Float64() float64
}

type reconnectPolicy struct {
	jitter   jitterSource
	failures int
	nextBase time.Duration
}

func newReconnectPolicy(jitter jitterSource) *reconnectPolicy {
	if jitter == nil {
		jitter = cryptoJitterSource{}
	}

	return &reconnectPolicy{
		jitter:   jitter,
		nextBase: reconnectInitialDelay,
	}
}

func (p *reconnectPolicy) Next() reconnectAttempt {
	baseDelay := p.nextBase
	p.failures++

	jitterFactor := (p.jitter.Float64()*2 - 1) * reconnectJitterRatio
	delay := time.Duration(float64(baseDelay) * (1 + jitterFactor))

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

type cryptoJitterSource struct{}

func (cryptoJitterSource) Float64() float64 {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		now := time.Now().UnixNano()
		return float64(uint64(now%1_000_000)) / 1_000_000
	}

	return float64(binary.BigEndian.Uint64(buf[:])) / float64(^uint64(0))
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
