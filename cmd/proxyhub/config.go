package main

import (
	"go.uber.org/zap/zapcore"
)

type ConfigProxy struct {
	Username string
	Password string
}

type Config struct {
	Log struct {
		Level zapcore.Level
	}

	Bind string
	Motd string

	Proxies   map[string]ConfigProxy
	APITokens []string
}

func (c *Config) FindProxyConfigByUsername(username string) (id string, proxy ConfigProxy, ok bool) {
	for id, proxy = range c.Proxies {
		if proxy.Username == username {
			ok = true
			return
		}
	}

	return
}

func (c *Config) FindAPIToken(token string) bool {
	for _, t := range c.APITokens {
		if t == token {
			return true
		}
	}

	return false
}
