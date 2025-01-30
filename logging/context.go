package logging

import (
	"context"

	"go.uber.org/zap"
)

var contextKey = &struct{}{}

func WithLogger(ctx context.Context, logger *zap.Logger) context.Context {
	return context.WithValue(ctx, contextKey, logger)
}

func FromContext(ctx context.Context, fields ...zap.Field) *zap.Logger {
	var res *zap.Logger
	if logger, ok := ctx.Value(contextKey).(*zap.Logger); ok {
		res = logger
	} else {
		res = zap.L()
	}
	return res.With(fields...)
}
