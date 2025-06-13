package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"reflect"
	"time"

	"github.com/spf13/cobra"
	"github.com/tarik02/proxyhub/logging"
	"github.com/tarik02/proxyhub/proxyclient"
	"github.com/tarik02/proxyhub/proxyclient/proxycatalog"
	"github.com/tarik02/proxyhub/proxyclient/proxydialer"
	"github.com/tarik02/proxyhub/socks"
	"go.uber.org/zap"
)

var fEndpoint, fToken string
var fProxyPort int

func getClientOptions() proxyclient.ClientOptions {
	opts := proxyclient.NewClientOptions()
	opts.Endpoint = fEndpoint
	opts.Token = fToken
	return opts
}

var rootCmd = &cobra.Command{
	Use:          "proxyclient",
	SilenceUsage: true,
	Version:      version,
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List available proxies",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		log := logging.FromContext(ctx)
		clientOptions := getClientOptions()

		client := proxycatalog.NewClient(ctx, clientOptions)

		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-client.CloseChan():
			return fmt.Errorf("client closed unexpectedly: %w", (client.Err()))

		case <-time.After(5 * time.Second):
			return fmt.Errorf("connection timed out, exiting")

		case <-client.Ready():
			log.Info("client is ready")

		case ev := <-client.Events():
			if ev, ok := ev.(proxycatalog.EventDisconnected); ok {
				return fmt.Errorf("client disconnected: %w", ev.Err)
			} else {
				return fmt.Errorf("unexpected event type: %s", reflect.TypeOf(ev).Name())
			}
		}

		log.Debug("waiting for first event")

		var initEvent proxycatalog.EventInit

		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-client.CloseChan():
			return fmt.Errorf("client closed unexpectedly: %w", (client.Err()))

		case <-time.After(15 * time.Second):
			return fmt.Errorf("waiting for event timed out, exiting")

		case event := <-client.Events():
			log.Debug("received event", zap.Any("event", event))

			if event, ok := event.(proxycatalog.EventInit); ok {
				initEvent = event
			} else {
				return fmt.Errorf("unexpected initial event type: %s", reflect.TypeOf(event).Name())
			}
		}

		log.Info("proxies list received")
		for _, p := range initEvent {
			os.Stdout.WriteString(fmt.Sprintf("ID: %s\n", p.ID))
			os.Stdout.WriteString(fmt.Sprintf("Version: %s\n", p.Version))
			os.Stdout.WriteString(fmt.Sprintf("Port: %d\n", p.Port))
			os.Stdout.WriteString(fmt.Sprintf("Started: %s\n", time.Unix(p.Started, 0).Format(time.RFC3339)))
			if len(p.EgressWhitelist) > 0 {
				os.Stdout.WriteString("Egress Whitelist:\n")
				for _, addr := range p.EgressWhitelist {
					os.Stdout.WriteString(fmt.Sprintf("  - %s\n", addr))
				}
			} else {
				os.Stdout.WriteString("Egress Whitelist: none\n")
			}
		}

		log.Debug("closing client")

		go func() {
			for range client.Events() {
				//
			}
		}()

		return client.Close()
	},
}

var proxyCmd = &cobra.Command{
	Use:   "proxy",
	Short: "Start a SOCKS5 proxy client",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		log := logging.FromContext(ctx)

		proxyDialer := proxydialer.Dialer{
			Options: getClientOptions(),
			ID:      args[0],
		}

		listener, err := net.Listen("tcp", fmt.Sprintf(":%d", fProxyPort))
		if err != nil {
			return fmt.Errorf("failed to listen: %w", (err))
		}

		log.Info("listening on", zap.String("address", listener.Addr().String()))

		socksServer := socks.Socks5Server{
			Dialer: &proxyDialer,
		}

		go func() {
			<-ctx.Done()
			log.Info("shutting down server")
			if err := listener.Close(); err != nil {
				log.Error("failed to close listener", zap.Error(err))
			}
		}()

		for {
			conn, err := listener.Accept()
			if err != nil {
				if !errors.Is(err, net.ErrClosed) {
					log.Error("failed to accept connection", zap.Error(err))
				}
				break
			}
			log.Info("accepted connection", zap.String("remote_addr", conn.RemoteAddr().String()))

			go func() {
				if err := socksServer.ServeConn(logging.WithLogger(ctx, log.Named("socks5")), conn); err != nil {
					log.Warn("socks5 server error", zap.Error(err))
				}
			}()
		}

		return nil
	},
}

func init() {
	rootCmd.PersistentFlags().StringVar(&fEndpoint, "endpoint", "", "ProxyHub endpoint")
	rootCmd.PersistentFlags().StringVar(&fToken, "token", "", "ProxyHub token")
	rootCmd.MarkPersistentFlagRequired("endpoint")
	rootCmd.MarkPersistentFlagRequired("token")

	proxyCmd.PersistentFlags().IntVar(&fProxyPort, "proxy-port", 1080, "Port to listen on for SOCKS5 connections")

	rootCmd.AddCommand(listCmd)
	rootCmd.AddCommand(proxyCmd)
}

func main() {
	ctx := context.Background()

	log, _ := zap.NewDevelopment()
	ctx = logging.WithLogger(ctx, log)
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()

	rootCmd.ExecuteContext(ctx)
}
