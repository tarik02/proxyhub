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
	"github.com/gin-contrib/pprof"
	"github.com/google/uuid"
	"github.com/hashicorp/yamux"
	"github.com/mitchellh/mapstructure"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/viper"
	"github.com/tarik02/proxyhub/api"
	"github.com/tarik02/proxyhub/entevents"
	"github.com/tarik02/proxyhub/logging"
	"github.com/tarik02/proxyhub/pb/pbhub"
	"github.com/tarik02/proxyhub/proxyhub"
	"github.com/tarik02/proxyhub/util"
	"github.com/tarik02/proxyhub/wsstream"
	bearertoken "github.com/vence722/gin-middleware-bearer-token"
	"google.golang.org/grpc"

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
		config.ApplyDefaults()
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

	switch config.Mode {
	case ConfigModeDevelopment:
		gin.SetMode(gin.DebugMode)

	case ConfigModeProduction:
		gin.SetMode(gin.ReleaseMode)
	}

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
				_ = p.Handler().SendMOTD(ctx, motd)
			}
		}()

		go func() {
			info, infoChanged := p.Handler().Info()
			if err := proxyEvents.Add(ctx, p.ID(), info); err != nil {
				log.Debug("proxyevents.Add failed", zap.Error(err))
			}

			for {
				select {
				case <-ctx.Done():
					return

				case <-p.CloseChan():
					if err := proxyEvents.Del(ctx, p.ID()); err != nil {
						log.Debug("proxyevents.Del failed", zap.Error(err))
					}
					return

				case <-infoChanged:
					info, infoChanged = p.Handler().Info()
					if err := proxyEvents.Update(ctx, "update", p.ID(), info, info); err != nil {
						log.Debug("proxyevents.Update failed", zap.Error(err))
					}
				}
			}
		}()
	}

	r.GET("/healthz", func(c *gin.Context) {
		select {
		case <-ctx.Done():
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "context cancelled"})
			return

		case <-shutdownChan:
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "shutting down"})
			return

		default:
		}

		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	r.GET("/metrics", func(ctx *gin.Context) {
		if !config.Metrics.Enabled {
			ctx.Status(http.StatusNotFound)
			return
		}

		promhttp.Handler().ServeHTTP(ctx.Writer, ctx.Request)
	})

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
		defer func() {
			_ = conn.Close()
		}()

		wsstream := wsstream.New(conn)
		yamuxConfig := yamux.DefaultConfig()
		logging.ConfigureYamuxLogger(yamuxConfig, log)
		session, err := yamux.Client(wsstream, yamuxConfig)
		if err != nil {
			log.Warn("yamux client failed", zap.Error(err))
			return
		}

		info := api.Proxy{
			ID:      id,
			Version: clientVersion,
			Started: time.Now().Unix(),
		}

		proxy, err := proxyhub.NewProxy(ctx, id, session, func(proxy *proxyhub.Proxy) (proxyhub.ProxyHandler, error) {
			if proxyhub.IsLegacyClientVersion(clientVersion) {
				log.Warn("client version is legacy, not expecting control stream", zap.String("clientVersion", clientVersion))
				return proxyhub.NewProxyHandlerLegacy(proxy, info), nil
			}

			control1, err := session.OpenStream()
			if err != nil {
				return nil, fmt.Errorf("opening control stream failed: %w", err)
			}
			go func() {
				<-proxy.CloseChan()
				_ = control1.Close()
			}()

			control2, err := session.OpenStream()
			if err != nil {
				return nil, fmt.Errorf("opening control stream failed: %w", err)
			}
			go func() {
				<-proxy.CloseChan()
				_ = control2.Close()
			}()

			grpcServer := grpc.NewServer()

			grpcClient, err := util.GrpcClientFromConn(control2)
			if err != nil {
				return nil, fmt.Errorf("grpc client from conn failed: %w", err)
			}
			go func() {
				<-proxy.CloseChan()
				log.Debug("closing gRPC client connection")
				_ = grpcClient.Close()
			}()

			grpcHandler := proxyhub.NewProxyHandlerGRPC(proxy, grpcClient, info)
			pbhub.RegisterServiceServer(grpcServer, grpcHandler)

			if err := util.GrpcServeOnConn(grpcServer, control1); err != nil {
				return nil, fmt.Errorf("grpc server serve failed: %w", err)
			}
			go func() {
				<-proxy.CloseChan()
				log.Debug("closing gRPC server")
				grpcServer.Stop()
			}()

			return grpcHandler, nil
		})
		if err != nil {
			log.Warn("creating proxy failed", zap.Error(err))
			return
		}

		proxy.OnConnection = func() {
			proxyConnsTotalMetric.WithLabelValues(proxy.ID()).Inc()
		}
		proxy.OnConnectionStats = func(recv, sent int64) {
			proxyRecvTotalMetric.WithLabelValues(proxy.ID()).Add(float64(recv))
			proxySentTotalMetric.WithLabelValues(proxy.ID()).Add(float64(sent))
		}

		if err := hub.HandleJoinProxy(ctx, proxy); err != nil {
			_ = proxy.Close()
			return
		}

		_ = proxy.Wait(ctx)
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

	r.GET("/proxy/:id/tunnel", tokenAuth, func(c *gin.Context) {
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
		yamuxConfig := yamux.DefaultConfig()
		logging.ConfigureYamuxLogger(yamuxConfig, log)
		session, err := yamux.Client(wsstream, yamuxConfig)
		if err != nil {
			log.Warn("yamux client failed", zap.Error(err))
			return
		}

		defer func() {
			_ = conn.Close()
		}()

		for {
			stream, err := session.AcceptStreamWithContext(ctx)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, proxyhub.ErrShutdown) {
					log.Debug("session closed or shutdown", zap.Error(err))
					return
				}
				log.Warn("accept stream failed", zap.Error(err))
				return
			}

			log.Debug("accepted stream", zap.String("id", id))

			if err := proxy.QueueConn(ctx, stream); err != nil {
				log.Warn("handle conn failed", zap.Error(err))
				return
			}
		}
	})

	r.GET("/api/proxies", tokenAuth, proxyEvents.ServeSnapshot)
	r.GET("/api/proxies/live", tokenAuth, proxyEvents.ServeSSE)

	if config.Profiling.Enabled {
		g := r.Group("", bearertoken.MiddlewareWithStaticToken(config.Profiling.Token))
		pprof.RouteRegister(g)
	}

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
