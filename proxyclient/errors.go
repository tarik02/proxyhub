package proxyclient

import (
	"errors"
	"fmt"
)

var ErrShutdown = errors.New("shutdown")
var ErrTransportClosed = errors.New("transport closed")
var ErrUnauthorized = errors.New("unauthorized")
var ErrNotFound = errors.New("not found")

type UnexpectedStatusError struct {
	StatusCode int
}

func (e *UnexpectedStatusError) Error() string {
	return fmt.Sprintf("unexpected status code: %d", e.StatusCode)
}
