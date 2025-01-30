package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/google/uuid"
	"github.com/mitchellh/mapstructure"
	"github.com/spf13/viper"
	"github.com/tarik02/proxyhub/api"
	"github.com/tarik02/proxyhub/entevents"
	"github.com/tarik02/proxyhub/logging"
	"github.com/tarik02/proxyhub/protocol"
	"github.com/tarik02/proxyhub/util"
	bearertoken "github.com/vence722/gin-middleware-bearer-token"

	ginzap "github.com/gin-contrib/zap"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	prettyconsole "github.com/thessem/zap-prettyconsole"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

func main() {
	ctx := context.Background()

	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()

	log := prettyconsole.NewLogger(zap.DebugLevel)
	defer func() {
		_ = log.Sync()
	}()

	log.Info("proxyhub", zap.String("version", version), zap.String("commit", commit), zap.String("build date", date))

	if err := run(ctx, &log); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal("error", zap.Error(err))
	}
}

func run(ctx context.Context, rootLog **zap.Logger) error {
	log := *rootLog

	viper.SetConfigName("proxyhub.yaml")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	if err := viper.ReadInConfig(); err != nil {
		// TODO: Create default config, log and exit
		// if errors.Is(err, &viper.ConfigFileNotFoundError{}) {
		// }
		log.Fatal("error reading config file", zap.Error(err))
	}

	var config Config
	unmarshalConfig := func() error {
		if err := viper.UnmarshalExact(&config, viper.DecodeHook(mapstructure.ComposeDecodeHookFunc(
			logging.StringToLogLevelHookFunc(),
			util.StringToGlobHookFunc(),
		))); err != nil {
			return fmt.Errorf("error unmarshalling config: %w", err)
		}
		return nil
	}
	if err := unmarshalConfig(); err != nil {
		return err
	}

	log = prettyconsole.NewLogger(config.Log.Level)
	*rootLog = log
	zap.ReplaceGlobals(log)

	ctx = logging.WithLogger(ctx, log)
	wg, ctx := errgroup.WithContext(ctx)

	r := gin.New()
	r.Use(ginzap.Ginzap(log, time.RFC3339, true))
	r.Use(ginzap.RecoveryWithZap(log, true))

	proxyMan := NewProxyManager()
	proxyEvents := entevents.New[api.Proxy]()

	proxyMan.OnProxyAdded = func(p *Proxy) {
		if motd := config.Motd; motd != "" {
			_ = p.QueueToWS(protocol.CmdMessage{
				Message: motd,
			})
		}

		proxyEvents.Add(p.ID(), api.Proxy{
			ID:                p.ID(),
			Ready:             p.Ready(),
			Port:              p.Port(),
			Uptime:            int64(time.Since(p.Started()).Seconds()),
			ActiveConnections: p.ActiveConnectionsCount(),
		})
	}

	proxyMan.OnProxyRemoved = func(p *Proxy) {
		proxyEvents.Del(p.ID())
	}

	tokenAuth := bearertoken.Middleware(func(s string, ctx *gin.Context) bool {
		return config.FindAPIToken(s)
	})

	r.GET("/connect", func(c *gin.Context) {
		rid := uuid.New().String()
		log := log.With(zap.String("rid", rid))

		username, password, ok := c.Request.BasicAuth()
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			log.Warn("unauthorized")
			return
		}

		id, proxy, ok := config.FindProxyConfigByUsername(username)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			log.Warn("invalid username")
			return
		}

		if password != proxy.Password {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			log.Warn("invalid password")
			return
		}

		var upgrader = websocket.Upgrader{}

		conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			log.Warn("upgrade failed", zap.Error(err))
			return
		}

		if err := proxyMan.HandleProxy(id, conn); err != nil {
			log.Warn("handle proxy failed", zap.Error(err))
			defer func() {
				_ = conn.Close()
			}()
			w, err2 := conn.NextWriter(websocket.BinaryMessage)
			if err2 != nil {
				log.Warn("next writer failed", zap.Error(err2))
				return
			}
			if err2 := protocol.WriteCmd(w, protocol.CmdMessage{
				Message: err.Error(),
			}); err2 != nil {
				log.Warn("write cmd failed", zap.Error(err2))
				return
			}
		}
	})

	r.GET("/api/proxies", proxyEvents.ServeSnapshot)
	r.GET("/api/proxies/live", tokenAuth, proxyEvents.ServeSSE)

	wg.Go(func() error {
		server := http.Server{
			Addr:              config.Bind,
			Handler:           r,
			ReadHeaderTimeout: 10 * time.Second,
		}

		wg.Go(func() error {
			if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		})

		<-ctx.Done()

		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()

		if err := server.Shutdown(ctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return err
		}

		if err := server.Close(); err != nil {
			return err
		}

		return nil
	})

	wg.Go(func() error {
		return proxyMan.Run(ctx)
	})

	wg.Go(func() error {
		return proxyEvents.Run(ctx)
	})

	viper.OnConfigChange(func(in fsnotify.Event) {
		wg.Go(unmarshalConfig)
	})

	go viper.WatchConfig()

	return wg.Wait()
}
