package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"alice/internal/app"
	"alice/internal/httpapi"
)

func main() {
	addr := flag.String("addr", ":8080", "management HTTP listen address")
	dataDir := flag.String("data", "data", "persistent Alice data directory")
	componentDir := flag.String("components", "components", "dynamic component source root")
	flag.Parse()

	alice, err := app.New(*dataDir, *componentDir)
	if err != nil {
		log.Fatal(err)
	}
	defer alice.Close()

	server := &http.Server{Addr: *addr, Handler: httpapi.New(alice).Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		fmt.Printf("Alice Core management: http://localhost%s\n", *addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
}
