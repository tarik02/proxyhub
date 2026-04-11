package proxyhub

import (
	"errors"
	"testing"
)

func TestClassifyProxyRemoval(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name            string
		err             error
		explicitReason  string
		hubShuttingDown bool
		want            string
	}{
		{
			name: "session closed",
			err:  ErrSessionClosed,
			want: "session_closed",
		},
		{
			name: "client disconnect",
			err:  errors.New("client initiated disconnect: client requested shutdown"),
			want: "client_disconnect",
		},
		{
			name:           "duplicate replaced",
			explicitReason: "duplicate_replaced",
			want:           "duplicate_replaced",
		},
		{
			name:            "server shutdown",
			err:             ErrShutdown,
			hubShuttingDown: true,
			want:            "server_shutdown",
		},
		{
			name: "generic error",
			err:  errors.New("boom"),
			want: "error",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := classifyProxyRemoval(tc.err, tc.explicitReason, tc.hubShuttingDown)
			if got != tc.want {
				t.Fatalf("expected reason %q, got %q", tc.want, got)
			}

			removal := newProxyRemoval(nil, tc.err, tc.explicitReason, tc.hubShuttingDown)
			if removal.reason != tc.want {
				t.Fatalf("expected removal reason %q, got %q", tc.want, removal.reason)
			}
		})
	}
}
