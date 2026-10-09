package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"factory/internal/factory"
)

func run(ctx context.Context, args []string) (err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("factoryd", flag.ContinueOnError)
	address := flags.String("addr", "127.0.0.1:8080", "HTTP listen address")
	data := flags.String("data", filepath.Join(home, ".factory"), "durable Factory data directory")
	web := flags.String("web", "web/dist", "production web build directory")
	allowPublic := flags.Bool("allow-public", false, "explicitly allow unauthenticated local execution API on non-loopback interfaces")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("factoryd accepts flags only")
	}
	listen, err := factory.ListenAddress(*address, *allowPublic)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	engine, err := factory.Open(*data)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, engine.Close()) }()
	server := &http.Server{Handler: engine.Handler(*web, *allowPublic), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	serving := make(chan error, 1)
	go func() { serving <- server.Serve(listener) }()
	if *allowPublic {
		slog.Warn("public access explicitly enabled: API has no authentication and can execute local commands")
	}
	slog.Info("Factory listening", "address", listener.Addr().String(), "data", *data, "web", *web)
	select {
	case <-ctx.Done():
	case err = <-engine.Errors():
	case err = <-serving:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	shutdownErr := server.Shutdown(shutdown)
	if shutdownErr != nil {
		shutdownErr = errors.Join(shutdownErr, server.Close())
	}
	return errors.Join(err, shutdownErr)
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "factoryd:", err)
		os.Exit(1)
	}
}
