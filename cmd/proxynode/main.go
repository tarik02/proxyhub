package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
	"github.com/tarik02/proxyhub/logging"
	"github.com/tarik02/proxyhub/socks"
	"github.com/tarik02/proxyhub/util"
	prettyconsole "github.com/thessem/zap-prettyconsole"
	"go.uber.org/zap"
	"golang.org/x/net/proxy"
)

var ErrWhitelisted = errors.New("whitelisted")

func main() {
	ctx := context.Background()

	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()

	log := prettyconsole.NewLogger(zap.DebugLevel)
	defer func() {
		_ = log.Sync()
	}()

	log.Info("proxynode", zap.String("version", version), zap.String("commit", commit), zap.String("build date", date))

	if err := run(ctx, &log); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal("error", zap.Error(err))
	}
}

func run(ctx context.Context, rootLog **zap.Logger) error {
	log := *rootLog

	viper.SetConfigName("proxynode.yaml")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	if err := viper.ReadInConfig(); err != nil {
		// TODO: Create default config, log and exit
		// if errors.Is(err, &viper.ConfigFileNotFoundError{}) {
		// }
		log.Fatal("error reading config file", zap.Error(err))
	}

	var config Config
	if err := viper.UnmarshalExact(&config, viper.DecodeHook(mapstructure.ComposeDecodeHookFunc(
		logging.StringToLogLevelHookFunc(),
		util.StringToGlobHookFunc(),
	))); err != nil {
		log.Fatal("error unmarshalling config", zap.Error(err))
	}

	log = prettyconsole.NewLogger(config.Log.Level)
	*rootLog = log
	zap.ReplaceGlobals(log)

	ctx = logging.WithLogger(ctx, log)

	t := time.NewTicker(1 * time.Second)
	defer t.Stop()

	isInWhitelist := func(target string) bool {
		for _, g := range config.EgressWhitelist {
			if g.Match(target) {
				return true
			}
		}
		return false
	}

	for {
		app := NewApp(AppParams{
			Version:  version,
			Endpoint: config.Endpoint,
			Username: config.Username,
			Password: config.Password,
		})

		app.Handler = func(conn *Connection) {
			var s socks.Socks5Server

			s.Dialer = proxy.FromEnvironment()

			s.ValidateTarget = func(ctx context.Context, target string) error {
				if !isInWhitelist(target) {
					log.Warn("whitelisted", zap.String("target", target))
					return ErrWhitelisted
				}
				return nil
			}

			if err := s.Run(logging.WithLogger(ctx, log.Named("socks5")), conn, conn); err != nil {
				log.Warn("socks5 server error", zap.Error(err))
			}
			if err := conn.Close(); err != nil {
				log.Warn("connection close error", zap.Error(err))
			}
		}

		app.OnServerMessage = func(message string) {
			log.Info("server message", zap.String("message", message))
		}

		if err := app.Run(ctx); err != nil {
			log.Error("app error", zap.Error(err))
		}

		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-t.C:
		}
	}
}
