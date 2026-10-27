package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/skyhook-io/radar/internal/portal"
	"github.com/skyhook-io/radar/internal/portalui"
)

const maxTokenMapBytes = 1 << 20

func main() {
	listen := flag.String("listen", "127.0.0.1:9290", "HTTP listen address")
	dataFile := flag.String("data-file", "./portal-data/registry.json", "Persistent registry JSON file")
	flag.Parse()

	credentials, err := loadCredentials()
	if err != nil {
		log.Fatal(err)
	}
	registry, err := portal.OpenRegistry(*dataFile)
	if err != nil {
		log.Fatalf("portal registry startup failed: %v", err)
	}
	static, err := fs.Sub(portalui.FS, "dist")
	if err != nil {
		log.Fatalf("portal web assets are unavailable; build the portal frontend and embed it")
	}
	handler, err := portal.NewHandler(registry, credentials, static)
	if err != nil {
		log.Fatalf("portal configuration is invalid: %v", err)
	}

	server := &http.Server{
		Addr:              *listen,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    10 << 10,
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("portal listen failed: %v", err)
	}
	log.Printf("Radar Portal listening on %s", listener.Addr())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("portal shutdown failed: %v", err)
		}
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("portal server failed: %v", err)
		}
	}
}

func loadCredentials() (portal.Credentials, error) {
	rawTokens := os.Getenv("RADAR_PORTAL_TOKENS")
	if len(rawTokens) > maxTokenMapBytes {
		return portal.Credentials{}, fmt.Errorf("RADAR_PORTAL_TOKENS exceeds %d bytes", maxTokenMapBytes)
	}
	var tokens map[string]string
	decoder := json.NewDecoder(io.LimitReader(strings.NewReader(rawTokens), maxTokenMapBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&tokens); err != nil || tokens == nil {
		return portal.Credentials{}, errors.New("RADAR_PORTAL_TOKENS must be a JSON object mapping cluster IDs to registration tokens")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return portal.Credentials{}, errors.New("RADAR_PORTAL_TOKENS must contain one JSON object")
	}
	return portal.Credentials{
		Tokens: tokens, Username: os.Getenv("RADAR_PORTAL_USERNAME"),
		Password: os.Getenv("RADAR_PORTAL_PASSWORD"),
	}, nil
}
