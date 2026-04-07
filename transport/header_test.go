package transport

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
)

func TestHeaderRoundTrip(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	doneCh := make(chan error, 1)
	go func() {
		doneCh <- WriteHeader(client, StreamHeader{
			Version: Version,
			Purpose: PurposeProxyTunnel,
			ProxyID: "proxy-1",
		})
	}()

	header, err := ReadHeader(server)
	if err != nil {
		t.Fatalf("ReadHeader() error = %v", err)
	}
	if err := <-doneCh; err != nil {
		t.Fatalf("WriteHeader() error = %v", err)
	}

	if header.Version != Version {
		t.Fatalf("Version = %d, want %d", header.Version, Version)
	}
	if header.Purpose != PurposeProxyTunnel {
		t.Fatalf("Purpose = %q, want %q", header.Purpose, PurposeProxyTunnel)
	}
	if header.ProxyID != "proxy-1" {
		t.Fatalf("ProxyID = %q, want %q", header.ProxyID, "proxy-1")
	}
}

func TestReadHeaderRejectsInvalidPayloads(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body []byte
		err  error
	}{
		{
			name: "empty header",
			body: nil,
			err:  ErrHeaderRequired,
		},
		{
			name: "invalid json",
			body: []byte(`{`),
			err:  ErrInvalidHeader,
		},
		{
			name: "unknown field",
			body: []byte(`{"version":1,"purpose":"grpc_control","extra":true}`),
			err:  ErrInvalidHeader,
		},
		{
			name: "unsupported purpose",
			body: []byte(`{"version":1,"purpose":"bogus"}`),
			err:  ErrUnsupportedType,
		},
		{
			name: "missing proxy id",
			body: []byte(`{"version":1,"purpose":"proxy_tunnel"}`),
			err:  ErrInvalidHeader,
		},
		{
			name: "proxy id not allowed",
			body: []byte(`{"version":1,"purpose":"grpc_control","proxy_id":"proxy-1"}`),
			err:  ErrInvalidHeader,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()

			go func() {
				var prefix [4]byte
				// tt.body is test-controlled and always much smaller than the 4-byte length prefix.
				//nolint:gosec
				binary.BigEndian.PutUint32(prefix[:], uint32(len(tt.body)))
				_, _ = client.Write(prefix[:])
				if len(tt.body) > 0 {
					_, _ = client.Write(tt.body)
				}
			}()

			_, err := ReadHeader(server)
			if !errors.Is(err, tt.err) {
				t.Fatalf("ReadHeader() error = %v, want %v", err, tt.err)
			}
		})
	}
}

func TestReadHeaderRejectsOversizedPayload(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		var prefix [4]byte
		binary.BigEndian.PutUint32(prefix[:], MaxHeaderSize+1)
		_, _ = client.Write(prefix[:])
	}()

	_, err := ReadHeader(server)
	if !errors.Is(err, ErrHeaderTooLarge) {
		t.Fatalf("ReadHeader() error = %v, want %v", err, ErrHeaderTooLarge)
	}
}

func TestWriteHeaderRejectsInvalidHeader(t *testing.T) {
	t.Parallel()

	err := WriteHeader(io.Discard, StreamHeader{
		Version: Version,
		Purpose: PurposeProxyTunnel,
	})
	if !errors.Is(err, ErrInvalidHeader) {
		t.Fatalf("WriteHeader() error = %v, want %v", err, ErrInvalidHeader)
	}
}
