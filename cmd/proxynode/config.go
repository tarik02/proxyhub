package main

import (
	"github.com/gobwas/glob"
	"go.uber.org/zap/zapcore"
)

type Config struct {
	Log struct {
		Level zapcore.Level
	}

	Endpoint string
	Username string
	Password string

	EgressWhitelist []glob.Glob
}
