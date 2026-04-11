package logging

import (
	"fmt"

	prettyconsole "github.com/thessem/zap-prettyconsole"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type Config struct {
	Pretty *bool
	Level  zapcore.Level
}

func (c *Config) CreateLogger() (*zap.Logger, error) {
	if c.Pretty == nil || *c.Pretty {
		return prettyconsole.NewLogger(c.Level), nil
	}

	cfg := zap.NewProductionConfig()
	cfg.Level = zap.NewAtomicLevelAt(c.Level)

	log, err := cfg.Build()
	if err != nil {
		return nil, fmt.Errorf("error creating logger: %w", err)
	}

	return log, nil
}
