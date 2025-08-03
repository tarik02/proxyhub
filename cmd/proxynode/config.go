package main

import (
	"github.com/gobwas/glob"
	"github.com/tarik02/proxyhub/logging"
)

type Config struct {
	Log logging.Config

	Endpoint string
	Username string
	Password string

	EgressWhitelist       []glob.Glob
	EgressWhitelistString []string `mapstructure:"egressWhitelist"`
}
