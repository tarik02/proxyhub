package main

import "github.com/tarik02/proxyhub/logging"

type ConfigMode string

const (
	ConfigModeDevelopment ConfigMode = "development"
	ConfigModeProduction  ConfigMode = "production"
)

type ConfigProxy struct {
	Username string
	Password string
}

type Config struct {
	Mode ConfigMode
	Log  logging.Config

	Bind string
	Motd string

	Metrics struct {
		Enabled bool
	}

	Proxies   map[string]ConfigProxy
	APITokens []string

	Profiling struct {
		Enabled bool
		Token   string
	}
}

func (c *Config) ApplyDefaults() {
	if c.Mode == "" {
		c.Mode = ConfigModeDevelopment
	}
	if c.Log.Pretty == nil {
		v := c.Mode == ConfigModeDevelopment
		c.Log.Pretty = &v
	}
}

func (c *Config) FindProxyConfigByID(id string) (proxy ConfigProxy, ok bool) {
	proxy, ok = c.Proxies[id]
	return
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
