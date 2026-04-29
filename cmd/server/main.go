package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aman7401/kv-store/internal/api"
	"github.com/aman7401/kv-store/internal/store"
	"github.com/aman7401/kv-store/internal/ttl"
	"github.com/aman7401/kv-store/internal/wal"
)

const (
	walPath     = "wal/kv.wal"
	listenAddr  = ":8080"
	ttlInterval = 5 * time.Second
)

func main() {
	if err := os.MkdirAll("wal", 0755); err != nil {
		log.Fatalf("create wal dir: %v", err)
	}

	kv := store.New()

	// Replay WAL to recover state after crash
	log.Println("replaying WAL...")
	if err := wal.Replay(walPath, func(r wal.Record) error {
		switch r.Op {
		case wal.OpSet:
			// Skip entries that would already be expired
			if !r.ExpiresAt.IsZero() && time.Now().After(r.ExpiresAt) {
				return nil
			}
			kv.SetWithExpiry(r.Key, r.Value, r.ExpiresAt)
		case wal.OpDelete:
			kv.Delete(r.Key)
		}
		return nil
	}); err != nil {
		log.Fatalf("wal replay: %v", err)
	}
	log.Printf("recovered %d keys from WAL", kv.Len())

	w, err := wal.Open(walPath)
	if err != nil {
		log.Fatalf("open wal: %v", err)
	}
	defer w.Close()

	// Background TTL sweeper
	cleaner := ttl.NewCleaner(
		ttlInterval,
		kv.Keys,
		func(key string) bool {
			_, err := kv.Get(key)
			return err != nil // expired or missing
		},
		kv.Delete,
	)
	cleaner.Start()
	defer cleaner.Stop()

	mux := http.NewServeMux()
	api.New(kv, w).RegisterRoutes(mux)

	srv := &http.Server{
		Addr:         listenAddr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	// Graceful shutdown on SIGINT / SIGTERM
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Printf("kv-store listening on %s", listenAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-quit
	log.Println("shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("server forced shutdown: %v", err)
	}
	log.Println("server stopped cleanly")
}
