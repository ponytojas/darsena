package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ponytojas/darsena/internal/kernel"
	"github.com/ponytojas/darsena/internal/modules/demo"
	"github.com/ponytojas/darsena/internal/transport"
)

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: darsena serve --demo | darsena worker")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	switch os.Args[1] {
	case "worker":
		log.Print("Worker skeleton ready; no infrastructure operations are implemented.")
		<-ctx.Done()
		return nil
	case "serve":
		flags := flag.NewFlagSet("serve", flag.ContinueOnError)
		isDemo := flags.Bool("demo", false, "explicitly enable volatile local simulations")
		addr := flags.String("addr", "127.0.0.1:7300", "loopback listen address")
		web := flags.String("web", "web/dist", "built frontend directory")
		if err := flags.Parse(os.Args[2:]); err != nil {
			return err
		}
		if !*isDemo {
			return fmt.Errorf("only demo mode is implemented; pass --demo")
		}
		if !transport.LoopbackAddress(*addr) {
			return fmt.Errorf("demo mode requires an explicit loopback IP address")
		}
		store := demo.New(700 * time.Millisecond)
		registry := kernel.New()
		if err := registry.Register(store); err != nil {
			return err
		}
		if err := registry.Start(ctx); err != nil {
			return err
		}
		server := &http.Server{Addr: *addr, Handler: transport.API{Store: store}.Handler(*web), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
		result := make(chan error, 1)
		go func() {
			log.Printf("Dársena DEMO at http://%s — fictitious data, no persistence", *addr)
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
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return serveErr
		}
		return stopErr
	default:
		return fmt.Errorf("unknown command %q", os.Args[1])
	}
}
