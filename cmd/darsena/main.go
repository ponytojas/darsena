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
	"strconv"
	"syscall"
	"time"

	"github.com/ponytojas/darsena/internal/kernel"
	"github.com/ponytojas/darsena/internal/modules/demo"
	"github.com/ponytojas/darsena/internal/platform/configvault"
	"github.com/ponytojas/darsena/internal/platform/ownerauth"
	"github.com/ponytojas/darsena/internal/platform/runtime/dockerlocal"
	"github.com/ponytojas/darsena/internal/platform/runtime/intent"
	"github.com/ponytojas/darsena/internal/platform/worker"
	"github.com/ponytojas/darsena/internal/transport"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey {
			return slog.Time(a.Key, a.Value.Time().UTC())
		}
		return a
	}})))
	if err := run(); err != nil {
		slog.Error("process_failed", "component", "process", "reason", err.Error())
		os.Exit(1)
	}
}
func run() error {
	cfg, err := resolveConfig(os.Args[1:], os.LookupEnv)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Println("usage: darsena serve (--demo | --experimental) [options] | darsena worker (--demo | --experimental) [options] | darsena owner-init --owner-credential path | darsena age-init --age-identity path")
		return nil
	}
	if err != nil {
		return err
	}
	if cfg.command == "owner-init" {
		return ownerauth.Generate(cfg.ownerCredential)
	}
	if cfg.command == "age-init" {
		return generateIdentity(cfg.identityFile)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	switch cfg.command {
	case "worker":
		service, err := startWorker(ctx, cfg)
		if err != nil {
			return err
		}
		defer service.Close()
		mode := "simulation"
		if cfg.experimental {
			mode = "experimental"
		}
		slog.Info("worker_started", "component", "worker", "mode", mode)
		err = service.Run(ctx)
		slog.Info("worker_stopped", "component", "worker")
		return err
	case "serve":
		store := demo.New(700 * time.Millisecond)
		registry := kernel.New()
		if err := registry.Register(store); err != nil {
			return err
		}
		if err := registry.Start(ctx); err != nil {
			return err
		}
		appsPort := 0
		if cfg.appsAddr != "" {
			_, port, _ := net.SplitHostPort(cfg.appsAddr)
			appsPort, _ = strconv.Atoi(port)
		}
		server := &http.Server{Addr: cfg.addr, Handler: transport.API{Store: store, WorkerSocket: cfg.workerSocket, Owner: cfg.owner, Installation: cfg.installation, AppsPort: appsPort}.Handler(cfg.web), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
		result := make(chan error, 2)
		var apps *http.Server
		if cfg.appsAddr != "" {
			apps = (&transport.AppProxy{WorkerSocket: cfg.workerSocket}).Server(cfg.appsAddr)
			go func() {
				slog.Info("apps_listener_started", "component", "server")
				result <- apps.ListenAndServe()
			}()
		}
		go func() {
			mode := "demo"
			if cfg.experimental {
				mode = "experimental"
			}
			slog.Info("server_started", "component", "server", "mode", mode)
			result <- server.ListenAndServe()
		}()
		var serveErr error
		select {
		case <-ctx.Done():
		case serveErr = <-result:
			cancel()
		}
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		stopErr := registry.Stop(stopCtx)
		_ = server.Shutdown(stopCtx)
		if apps != nil {
			_ = apps.Shutdown(stopCtx)
		}
		slog.Info("server_stopped", "component", "server")
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return errors.New("HTTP server failed")
		}
		return stopErr
	default:
		return errors.New("unknown command")
	}
}

func startWorker(ctx context.Context, cfg config) (*worker.Service, error) {
	var adapter *dockerlocal.Adapter
	if cfg.experimental {
		var err error
		adapter, err = dockerlocal.New(cfg.dockerSocket)
		if err != nil {
			return nil, errors.New("invalid rootless runtime socket or permissions")
		}
	}
	// Open the vault before recovering jobs: an invalid vault must not interrupt
	// previously running work or create the worker socket.
	var vault *configvault.Vault
	var err error
	if cfg.configDB != "" {
		vault, err = configvault.Open(ctx, cfg.configDB, cfg.identity)
		if err != nil {
			return nil, errors.New("cannot open encrypted configuration database; check its path, permissions, identity and contents")
		}
	}
	var intents *intent.Store
	if cfg.experimental {
		intents, err = intent.Open(ctx, cfg.intentDB)
		if err != nil {
			if vault != nil {
				_ = vault.Close()
			}
			return nil, errors.New("cannot open runtime intent database")
		}
	}
	service, err := worker.Open(ctx, cfg.state, cfg.socket)
	if err != nil {
		if intents != nil {
			_ = intents.Close()
		}
		if vault != nil {
			_ = vault.Close()
		}
		return nil, errors.New("cannot start worker; check state and socket paths, permissions and whether another worker is running")
	}
	if vault != nil {
		if err = service.EnableConfig(vault); err != nil {
			_ = vault.Close()
			_ = service.Close()
			if intents != nil {
				_ = intents.Close()
			}
			return nil, errors.New("cannot enable encrypted configuration")
		}
	}
	if cfg.experimental {
		if err = service.EnableRuntime(cfg.installation, intents, dockerlocal.PrepareRef, adapter); err != nil {
			_ = intents.Close()
			_ = service.Close()
			return nil, errors.New("cannot enable experimental runtime")
		}
	}
	return service, nil
}
