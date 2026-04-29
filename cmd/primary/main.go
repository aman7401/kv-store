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
	"github.com/aman7401/kv-store/internal/wal"
)

const walPath = "wal/primary.wal"

func main() {
	addr := flag.String("addr", ":8080", "primary listen address")
	flag.Parse()

	if err := os.MkdirAll("wal", 0755); err != nil {
		log.Fatalf("wal dir: %v", err)
	}

	kv := store.New()

	// Replay WAL on restart
	wal.Replay(walPath, func(r wal.Record) error {
		switch r.Op {
		case wal.OpSet:
			if !r.ExpiresAt.IsZero() && time.Now().After(r.ExpiresAt) {
				return nil
			}
			kv.SetWithExpiry(r.Key, r.Value, r.ExpiresAt)
		case wal.OpDelete:
			kv.Delete(r.Key)
		}
		return nil
	})

	w, err := wal.Open(walPath)
	if err != nil {
		log.Fatalf("open wal: %v", err)
	}
	defer w.Close()

	primary := replication.NewPrimary(kv, *addr)

	// Wrap API handler so writes also go through replication log
	mux := http.NewServeMux()
	primary.RegisterRoutes(mux)

	// Regular KV HTTP API (reads from local store, writes via primary)
	api.New(kv, w).RegisterRoutes(mux)

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
