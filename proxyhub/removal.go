package proxyhub

import (
	"errors"
	"strings"
)

type proxyRemoval struct {
	proxy  *Proxy
	err    error
	reason string
}

func newProxyRemoval(proxy *Proxy, err error, explicitReason string, hubShuttingDown bool) proxyRemoval {
	return proxyRemoval{
		proxy:  proxy,
		err:    err,
		reason: classifyProxyRemoval(err, explicitReason, hubShuttingDown),
	}
}

func classifyProxyRemoval(err error, explicitReason string, hubShuttingDown bool) string {
	if explicitReason != "" {
		return explicitReason
	}

	switch {
	case errors.Is(err, ErrSessionClosed):
		return "session_closed"
	case errors.Is(err, ErrDuplicateReplaced):
		return "duplicate_replaced"
	case errors.Is(err, ErrShutdown) && hubShuttingDown:
		return "server_shutdown"
	case err != nil && strings.HasPrefix(err.Error(), "client initiated disconnect:"):
		return "client_disconnect"
	default:
		return "error"
	}
}

func includeProxyRemovalError(removal proxyRemoval) bool {
	switch removal.reason {
	case "session_closed", "server_shutdown", "duplicate_replaced":
		return false
	default:
		return removal.err != nil
	}
}
