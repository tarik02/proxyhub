package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/elazarl/goproxy"
	"github.com/fsnotify/fsnotify"
	"github.com/google/uuid"
	"github.com/hashicorp/yamux"
	"github.com/mitchellh/mapstructure"
	"github.com/spf13/viper"
	"github.com/tarik02/proxyhub/api"
	"github.com/tarik02/proxyhub/entevents"
	"github.com/tarik02/proxyhub/logging"
	"github.com/tarik02/proxyhub/proxyhub"
	"github.com/tarik02/proxyhub/util"
	"github.com/tarik02/proxyhub/wsstream"
	bearertoken "github.com/vence722/gin-middleware-bearer-token"

	ginzap "github.com/gin-contrib/zap"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	prettyconsole "github.com/thessem/zap-prettyconsole"
	"go.uber.org/zap"
)

func main() {
	ctx := context.Background()

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
	shutdownChan := make(chan struct{})
	doneCh := make(chan struct{})
	errCh := make(chan error)
	errOnce := sync.Once{}

	defer close(doneCh)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

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

	unmarshalConfig := func() (Config, error) {
		var config Config
		if err := viper.UnmarshalExact(&config, viper.DecodeHook(mapstructure.ComposeDecodeHookFunc(
			logging.StringToLogLevelHookFunc(),
			util.StringToGlobHookFunc(),
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

	log = prettyconsole.NewLogger(config.Log.Level)
	*rootLog = log
	zap.ReplaceGlobals(log)

	ctx = logging.WithLogger(ctx, log)
	// wg, ctx := errgroup.WithContext(ctx)

	r := gin.New()
	r.Use(ginzap.Ginzap(log, time.RFC3339, true))
	r.Use(ginzap.RecoveryWithZap(log, true))

	proxyEvents := entevents.New[api.Proxy](ctx)

	tokenAuth := bearertoken.Middleware(func(s string, ctx *gin.Context) bool {
		return config.FindAPIToken(s)
	})

	hub := proxyhub.New(ctx)
	hub.ValidateAPIToken = config.FindAPIToken

	hub.OnProxyAdded = func(p *proxyhub.Proxy) {
		go func() {
			if motd := config.Motd; motd != "" {
				_ = p.SendMOTD(motd)
			}
		}()

		go func() {
			if err := proxyEvents.Add(ctx, p.ID(), api.Proxy{
				ID:      p.ID(),
				Version: p.Version(),
				Port:    p.Port(),
				Started: p.Started().Unix(),
			}); err != nil {
				log.Debug("proxyevents.Add failed", zap.Error(err))
			}
		}()
	}

	hub.OnProxyRemoved = func(p *proxyhub.Proxy) {
		go func() {
			if err := proxyEvents.Del(ctx, p.ID()); err != nil {
				log.Debug("proxyevents.Del failed", zap.Error(err))
			}
		}()
	}

	r.GET("/join", func(c *gin.Context) {
		rid := uuid.New().String()
		log := log.With(zap.String("rid", rid))

		username, password, ok := c.Request.BasicAuth()
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			log.Warn("unauthorized")
			return
		}

		id, proxyCfg, ok := config.FindProxyConfigByUsername(username)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			log.Warn("invalid username")
			return
		}

		if password != proxyCfg.Password {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			log.Warn("invalid password")
			return
		}

		clientVersion := c.GetHeader("X-Version")

		var upgrader = websocket.Upgrader{}

		conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			log.Warn("upgrade failed", zap.Error(err))
			return
		}

		var lc net.ListenConfig
		listener, err := lc.Listen(ctx, "tcp", "0.0.0.0:0")
		if err != nil {
			log.Warn("listen failed", zap.Error(err))
			_ = conn.Close()
			return
		}

		wsstream := wsstream.New(conn)
		session, err := yamux.Client(wsstream, yamux.DefaultConfig())
		if err != nil {
			_ = listener.Close()
			_ = conn.Close()
			return
		}

		proxy := proxyhub.NewProxy(ctx, id, clientVersion, listener, wsstream, session)

		if err := hub.HandleJoinProxy(ctx, proxy); err != nil {
			_ = proxy.Close()
			return
		}
	})

	r.GET("/socks/:id", tokenAuth, func(c *gin.Context) {
		id := c.Param("id")

		proxy := hub.GetProxyByID(id)
		if proxy == nil {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "proxy not found"})
			return
		}

		var upgrader = websocket.Upgrader{}

		conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			log.Warn("upgrade failed", zap.Error(err))
			return
		}

		wsstream := wsstream.New(conn)

		if err := proxy.QueueConn(ctx, wsstream); err != nil {
			log.Warn("handle conn failed", zap.Error(err))
			_ = conn.Close()
			return
		}
	})

	r.GET("/api/proxies", tokenAuth, proxyEvents.ServeSnapshot)
	r.GET("/api/proxies/live", tokenAuth, proxyEvents.ServeSSE)

	httpProxy := goproxy.NewProxyHttpServer()
	httpProxy.NonproxyHandler = r

	httpProxy.OnRequest().DoFunc(hub.ProxyHandler())
	httpProxy.OnRequest().HandleConnectFunc(hub.ProxyConnectHandler())

	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", config.Bind)
	if err != nil {
		_ = hub.Close()
		_ = proxyEvents.Close()
		return err
	}

	server := http.Server{
		Handler:           httpProxy,
		ReadHeaderTimeout: 10 * time.Second,
	}

	var wg sync.WaitGroup

	wg.Add(7)

	go func() {
		defer wg.Done()
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server error", zap.Error(err))
		}
		log.Debug("server.Serve returned")
	}()

	serverShutdownDoneCh := make(chan struct{})
	go func() {
		defer wg.Done()
		defer close(serverShutdownDoneCh)
		select {
		case <-ctx.Done():
			return
		case <-shutdownChan:
		}
		_ = server.Shutdown(ctx)
		log.Debug("server shutdown done")
	}()

	go func() {
		defer wg.Done()
		select {
		case <-doneCh:
			return
		case <-serverShutdownDoneCh:
			return
		case <-ctx.Done():
		}
		_ = server.Close()
		log.Debug("server close done")
	}()

	go func() {
		defer wg.Done()
		if err := hub.Wait(ctx); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, proxyhub.ErrShutdown) {
			log.Error("hub error", zap.Error(err))
			errOnce.Do(func() {
				errCh <- err
			})
		}
	}()

	go func() {
		defer wg.Done()
		if err := proxyEvents.Wait(ctx); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, entevents.ErrShutdown) {
			log.Error("proxyevents error", zap.Error(err))
			errOnce.Do(func() {
				errCh <- err
			})
		}
	}()

	go func() {
		defer wg.Done()
		select {
		case <-ctx.Done():
			return
		case <-shutdownChan:
		}
		_ = hub.Close()
	}()

	go func() {
		defer wg.Done()
		select {
		case <-ctx.Done():
			return
		case <-shutdownChan:
		}
		_ = proxyEvents.Close()
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

	go func() {
		s := make(chan os.Signal, 1)
		signal.Notify(s, os.Interrupt)
		defer signal.Stop(s)

		select {
		case <-ctx.Done():
			return
		case <-s:
			log.Info("got interrupt signal, shutting down gracefully with 60 seconds timeout")
		case <-errCh:
			log.Info("got error, shutting down gracefully with 60 seconds timeout")
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

	log.Info("server running", zap.String("addr", listener.Addr().String()))

	wg.Wait()

	return nil
}
