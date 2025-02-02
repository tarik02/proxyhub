package proxyhub

import "errors"

var ErrShutdown = errors.New("shutdown")
var ErrSessionClosed = errors.New("session closed")
