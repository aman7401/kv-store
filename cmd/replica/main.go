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

	"github.com/aman7401/kv-store/internal/replication"
	"github.com/aman7401/kv-store/internal/store"
)

func main() {
	addr := flag.String("addr", ":8081", "replica listen address")
	primaryAddr := flag.String("primary", "localhost:8080", "primary address (host:port)")
	flag.Parse()

	kv := store.New()
	replica := replication.NewReplica(kv, *primaryAddr, *addr)
	replica.Start()

	mux := http.NewServeMux()
	replica.RegisterRoutes(mux)

	srv := &http.Server{
		Addr:         *addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Printf("replica listening on %s, pulling from primary %s", *addr, *primaryAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-quit
	log.Println("shutting down replica...")
	replica.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	log.Println("replica stopped")
}
