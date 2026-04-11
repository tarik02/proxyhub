package logging

import (
	"strings"

	"github.com/hashicorp/yamux"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type YamuxLogger struct {
	*zap.Logger
}

func NewYamuxLogger(logger *zap.Logger) *YamuxLogger {
	return &YamuxLogger{
		Logger: logger.WithOptions(zap.AddCallerSkip(1)).Named("yamux"),
	}
}

func ConfigureYamuxLogger(config *yamux.Config, logger *zap.Logger) {
	config.Logger = NewYamuxLogger(logger)
	config.LogOutput = nil
}

func (l *YamuxLogger) processMessage(msg string) (string, zapcore.Level) {
	level := zap.InfoLevel
	ok := false

	if msg, ok = strings.CutPrefix(msg, "[ERR] "); ok {
		level = zap.ErrorLevel
	} else if msg, ok = strings.CutPrefix(msg, "[WARN] "); ok {
		level = zap.WarnLevel
	}

	msg = strings.TrimPrefix(msg, "yamux: ")

	return msg, level
}

func (l *YamuxLogger) Print(v ...any) {
	if len(v) > 0 && v[0] != nil {
		if msg, ok := v[0].(string); ok {
			msgProcessed, level := l.processMessage(msg)
			rest := v[1:]
			l.Logger.Sugar().Log(level, msgProcessed, rest)
			return
		}
	}

	l.Logger.Sugar().Log(zap.InfoLevel, v...)
}

func (l *YamuxLogger) Printf(format string, v ...any) {
	logFormat, level := l.processMessage(format)

	l.Logger.Sugar().Logf(level, logFormat, v...)
}

func (l *YamuxLogger) Println(v ...any) {
	v = append([]any{"yamux: "}, v...)
	l.Print(v...)
}

var _ yamux.Logger = (*YamuxLogger)(nil)
