package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
)

type config struct {
	httpAddr      string
	dbPath        string
	adminEmail    string
	adminPassword string
	mqttAddr      string
	controllerID  string
	sso           ssoConfig
}

func loadConfig() config {
	return config{
		httpAddr:      envOr("BELEPTETO_HTTP_ADDR", ":8080"),
		dbPath:        envOr("BELEPTETO_DB_PATH", "belepteto.db"),
		adminEmail:    envOr("BELEPTETO_ADMIN_EMAIL", "admin@example.com"),
		adminPassword: os.Getenv("BELEPTETO_ADMIN_PASSWORD"),
		mqttAddr:      envOr("BELEPTETO_MQTT_ADDR", ":1883"),
		controllerID:  envOr("BELEPTETO_CONTROLLER_ID", "1619"),
		sso: ssoConfig{
			issuerURL:       os.Getenv("ISSUER_URL"),
			wellKnownURL:    os.Getenv("WELL_KNOWN_URL"),
			clientID:        os.Getenv("CLIENT_ID"),
			clientSecret:    os.Getenv("CLIENT_SECRET"),
			scopes:          os.Getenv("SCOPES"),
			redirectURL:     os.Getenv("SSO_REDIRECT_URL"),
			frontendURL:     os.Getenv("SSO_FRONTEND_URL"),
			loginButtonText: envOr("LOGIN_BUTTON_TEXT", defaultSSOLoginButtonText),
		},
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// seedAdmin creates the first admin user when the database is empty. If no
// password was supplied via env, a random one is generated and logged once so
// the operator can log in.
func seedAdmin(logger *slog.Logger, store *Store, cfg config) error {
	password := cfg.adminPassword
	generated := false
	if password == "" {
		b := make([]byte, 18)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		password = base64.RawURLEncoding.EncodeToString(b)
		generated = true
	}

	created, err := store.SeedAdmin(cfg.adminEmail, password)
	if err != nil {
		return err
	}
	if created && generated {
		logger.Warn("seeded initial admin user — save this password, it is shown only once",
			"email", cfg.adminEmail, "password", password)
	} else if created {
		logger.Info("seeded initial admin user", "email", cfg.adminEmail)
	}
	return nil
}

func logLevel() slog.Level {
	switch strings.ToLower(os.Getenv("BELEPTETO_LOG_LEVEL")) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func main() {
	if err := loadDotEnv(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel()}))
	if err := run(logger); err != nil {
		logger.Error("server exited with error", "err", err)
		os.Exit(1)
	}
}

// loadDotEnv loads local development configuration when .env is present.
// Process environment variables take precedence over values from the file.
func loadDotEnv() error {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("load .env: %w", err)
	}
	return nil
}

func run(logger *slog.Logger) error {
	cfg := loadConfig()

	store, err := OpenStore(cfg.dbPath)
	if err != nil {
		return err
	}
	logger.Info("database ready", "path", cfg.dbPath)

	if err := seedAdmin(logger, store, cfg); err != nil {
		return err
	}

	tmpl, err := LoadTemplates()
	if err != nil {
		return err
	}

	sso, err := newOIDCAuthenticator(context.Background(), cfg.sso, nil)
	if err != nil {
		return fmt.Errorf("configure OIDC SSO: %w", err)
	}

	broker, err := NewBroker(logger, cfg.mqttAddr)
	if err != nil {
		return err
	}
	if err := broker.Start(); err != nil {
		return fmt.Errorf("start mqtt broker: %w", err)
	}
	logger.Info("mqtt broker listening", "addr", cfg.mqttAddr, "controller", cfg.controllerID)

	access := NewAccessService(store, broker, NewControllerTopics(cfg.controllerID), logger)
	if err := access.Start(); err != nil {
		_ = broker.Close()
		return fmt.Errorf("start access service: %w", err)
	}

	srv := &http.Server{
		Addr:              cfg.httpAddr,
		Handler:           newRouter(logger, tmpl, store, access, sso),
		ReadHeaderTimeout: 10 * time.Second,
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "addr", cfg.httpAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	select {
	case err := <-serverErr:
		access.Close()
		_ = broker.Close()
		return err
	case <-rootCtx.Done():
		logger.Info("shutdown signal received, draining connections")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	access.Close()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		_ = broker.Close()
		return err
	}

	if err := broker.Close(); err != nil {
		logger.Error("mqtt broker close", "err", err)
	}

	logger.Info("shutdown complete")
	return nil
}
