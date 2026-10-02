package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"nacfson_pipeline/gateway/internal/auth"
	"nacfson_pipeline/gateway/internal/config"
	"nacfson_pipeline/gateway/internal/keycloak"
)

func main() {
	log.Println("Starting Personal Project Platform Authentication Gateway...")

	cfg, err := config.LoadFromEnv()
	if err != nil {
		log.Fatalf("Configuration error: %v", err)
	}

	kcClient := keycloak.NewClient(
		cfg.KeycloakIssuerURL,
		cfg.KeycloakAdminURL,
		cfg.ClientID,
		cfg.ClientSecret,
	)

	handler := auth.NewHandler(cfg, kcClient, kcClient)

	mux := http.NewServeMux()

	// Liveness / readiness probe for Kubernetes
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	// Traefik ForwardAuth verification endpoint
	mux.HandleFunc("/auth", handler.HandleForwardAuth)

	// OIDC callback from Keycloak
	mux.HandleFunc("/oauth/callback", handler.HandleCallback)

	// User-initiated synchronous session revocation
	mux.HandleFunc("/auth/logout", handler.HandleLogout)

	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  30 * time.Second,
	}

	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("Gateway listening on :%d\n", cfg.Port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server ListenAndServe error: %v", err)
		}
	}()

	<-stopChan
	log.Println("Shutting down gateway gracefully...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Printf("Server forced to shutdown: %v\n", err)
	}
	log.Println("Gateway stopped cleanly.")
}
