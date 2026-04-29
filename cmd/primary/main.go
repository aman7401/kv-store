package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aman7401/kv-store/internal/api"
	"github.com/aman7401/kv-store/internal/replication"
	"github.com/aman7401/kv-store/internal/store"
)

func main() {
	addr := flag.String("addr", ":8080", "primary listen address")
	flag.Parse()

	kv := store.New()

	primary := replication.NewPrimary(kv, *addr)

	mux := http.NewServeMux()
	primary.RegisterRoutes(mux)                  // /replication/* endpoints
	api.NewPrimary(kv, primary).RegisterRoutes(mux) // /get /set /delete — writes go through replication log

	srv := &http.Server{
		Addr:         *addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Printf("primary listening on %s", *addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-quit
	log.Println("shutting down primary...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	log.Println("primary stopped")
}
