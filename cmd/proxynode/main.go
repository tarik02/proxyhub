package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/go-viper/mapstructure/v2"
	"github.com/hashicorp/yamux"
	"github.com/spf13/viper"
	"github.com/tarik02/proxyhub/logging"
	"github.com/tarik02/proxyhub/proxynode"
	"github.com/tarik02/proxyhub/socks"
	"github.com/tarik02/proxyhub/util"
	prettyconsole "github.com/thessem/zap-prettyconsole"
	"go.uber.org/zap"
	"golang.org/x/net/proxy"
)

var ErrWhitelisted = errors.New("whitelisted")

func main() {
	ctx := context.Background()

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
	shutdownChan := make(chan struct{})
	doneCh := make(chan struct{})
	defer close(doneCh)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

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

	unmarshalConfig := func() (Config, error) {
		var config Config
		if err := viper.UnmarshalExact(&config, viper.DecodeHook(mapstructure.ComposeDecodeHookFunc(
			logging.StringToLogLevelHookFunc(),
			util.StringToGlobHookFunc('.', ':'),
		))); err != nil {
			return config, fmt.Errorf("error unmarshalling config: %w", err)
		}
		return config, nil
	}

	var config Config
	if c, err := unmarshalConfig(); err != nil {
		return err
	} else {
		config = c
	}

	log, err := config.Log.CreateLogger()
	if err != nil {
		return err
	}
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

	var s socks.Socks5Server

	s.Dialer = proxy.FromEnvironment()
	s.ValidateTarget = func(ctx context.Context, target string) error {
		if !isInWhitelist(target) {
			log.Warn("whitelisted", zap.String("target", target))
			return ErrWhitelisted
		}
		return nil
	}

	var wg sync.WaitGroup

	go func() {
		s := make(chan os.Signal, 1)
		signal.Notify(s, os.Interrupt)
		defer signal.Stop(s)

		select {
		case <-ctx.Done():
			return
		case <-s:
			log.Info("got interrupt signal, shutting down gracefully with 60 seconds timeout")
		}

		log.Info("next interrupt signal will force shutdown")
		close(shutdownChan)

		select {
		case <-ctx.Done():
			return
		case <-s:
			log.Warn("got interrupt signal again, forcing shutdown")
		case <-time.After(60 * time.Second):
			log.Warn("shutdown timeout reached, forcing shutdown")
		}

		log.Info("next interrupt signal will terminate the process")
		cancel()

		select {
		case <-doneCh:
			return

		case <-s:
			log.Fatal("got interrupt signal again, terminating the process")

		case <-time.After(5 * time.Second):
			log.Fatal("shutdown timeout reached, terminating the process")
		}
	}()

	viper.OnConfigChange(func(in fsnotify.Event) {
		wg.Add(1)
		defer wg.Done()
		if c, err := unmarshalConfig(); err != nil {
			log.Warn("error reloading config", zap.Error(err))
		} else {
			log.Info("config reloaded")
			config = c
		}
	})

	go viper.WatchConfig()

	defer wg.Wait()

	log.Info("application running")

loop:
	for {
		app := proxynode.New(ctx, proxynode.Params{
			Version:         version,
			Endpoint:        config.Endpoint,
			Username:        config.Username,
			Password:        config.Password,
			EgressWhitelist: config.EgressWhitelistString,
		})

		app.Handler = func(conn *yamux.Stream) {
			log.Info("new connection", zap.Uint32("id", conn.StreamID()))

			if err := s.ServeConn(logging.WithLogger(ctx, log.Named("socks5")), conn); err != nil {
				log.Warn("socks5 server error", zap.Error(err))
			}
		}

		app.OnServerMessage = func(message string) {
			log.Info("server message", zap.String("message", message))
		}

		wg.Add(2)

		go func() {
			defer wg.Done()

			select {
			case <-ctx.Done():
				return
			case <-app.CloseChan():
				return
			case <-shutdownChan:
			}

			_ = app.Close()
		}()

		go func() {
			defer wg.Done()

			if err := app.Wait(ctx); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, proxynode.ErrShutdown) {
				log.Error("app error", zap.Error(err))
			}
		}()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-shutdownChan:
			break loop
		case <-app.CloseChan():
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-shutdownChan:
			break loop
		case <-t.C:
		}
	}

	return nil
}
