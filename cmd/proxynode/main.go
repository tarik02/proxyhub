package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
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
	configChanged := make(chan struct{})
	configMu := &sync.RWMutex{}

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

	isInWhitelist := func(target string) bool {
		configMu.RLock()
		defer configMu.RUnlock()

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
	defer wg.Wait()

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

		c, err := unmarshalConfig()
		if err != nil {
			log.Warn("error reloading config", zap.Error(err))
			return
		}

		log.Info("config reloaded")

		configMu.Lock()
		config = c
		close(configChanged)
		configChanged = make(chan struct{})
		configMu.Unlock()
	})

	go viper.WatchConfig()

	log.Info("application running")

	reconnects := newReconnectPolicy(nil)
	var lastErr error

loop:
	for {
		configMu.RLock()
		currentConfig := config
		configChangedCh := configChanged
		configMu.RUnlock()

		connectFields := []zap.Field{
			zap.String("endpoint", currentConfig.Endpoint),
		}
		if lastErr != nil {
			connectFields = append(connectFields,
				zap.String("previous_reason", classifyReconnectError(lastErr)),
				zap.Error(lastErr),
			)
		}
		log.Info("connecting to server", connectFields...)

		app := proxynode.New(ctx, proxynode.Params{
			Version:         version,
			Endpoint:        currentConfig.Endpoint,
			Username:        currentConfig.Username,
			Password:        currentConfig.Password,
			EgressWhitelist: currentConfig.EgressWhitelistString,
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

		connectedCh := make(chan struct{}, 1)
		var connectedSeen atomic.Bool
		app.OnConnected = func() {
			connectedSeen.Store(true)
			select {
			case connectedCh <- struct{}{}:
			default:
			}
		}

		waitErrCh := make(chan error, 1)
		go func() {
			waitErrCh <- app.Wait(ctx)
		}()

		connected := false

		for {
			select {
			case <-ctx.Done():
				_ = app.Close()
				return ctx.Err()

			case <-shutdownChan:
				_ = app.Close()
				if err := <-waitErrCh; err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, proxynode.ErrShutdown) {
					log.Debug("app stopped during shutdown", zap.Error(err))
				}
				break loop

			case <-configChangedCh:
				configMu.RLock()
				configChangedCh = configChanged
				whitelist := append([]string(nil), config.EgressWhitelistString...)
				configMu.RUnlock()

				log.Debug("config changed, sending new egress whitelist")
				if err := app.UpdateEgressWhitelist(ctx, whitelist); err != nil {
					if ctx.Err() != nil || errors.Is(err, proxynode.ErrShutdown) {
						continue
					}
					log.Error("error updating egress whitelist", zap.Error(err))
				}

			case <-connectedCh:
				if connected {
					continue
				}

				connected = true
				lastErr = nil
				reconnects.Reset()
				log.Info("connected to server", zap.String("endpoint", currentConfig.Endpoint))

			case err := <-waitErrCh:
				if connectedSeen.Load() && !connected {
					connected = true
					reconnects.Reset()
				}

				if err == nil || errors.Is(err, proxynode.ErrShutdown) {
					break loop
				}
				if errors.Is(err, context.Canceled) {
					return err
				}

				lastErr = err
				attempt := reconnects.Next()
				phase := "startup"
				if connected {
					phase = "session"
				}

				log.Error("app error",
					zap.String("reason", classifyReconnectError(err)),
					zap.String("phase", phase),
					zap.Int("retry_attempt", attempt.Number),
					zap.Duration("retry_delay", attempt.Delay),
					zap.Error(err),
				)

				timer := time.NewTimer(attempt.Delay)
				select {
				case <-ctx.Done():
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					return ctx.Err()

				case <-shutdownChan:
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					break loop

				case <-timer.C:
				}

				continue loop
			}
		}
	}

	return nil
}
