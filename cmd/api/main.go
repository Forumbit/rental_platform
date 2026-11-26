package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/forumbit/rental_platform/internal/auth"
	"github.com/forumbit/rental_platform/internal/config"
	"github.com/forumbit/rental_platform/internal/handler"
	"github.com/forumbit/rental_platform/internal/mail"
	"github.com/forumbit/rental_platform/internal/repository"
	"github.com/forumbit/rental_platform/internal/service"
	"github.com/forumbit/rental_platform/internal/storage"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	// Получение конфигурации
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	tokens, err := auth.NewTokenManager(cfg.Auth)
	if err != nil {
		return err
	}
	mailer, err := mail.NewSMTPMailer(cfg.SMTP)
	if err != nil {
		return err
	}

	// Соединение с базой данных
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	db, err := storage.Open(ctx, cfg.Database)
	cancel()
	if err != nil {
		return err
	}
	defer db.Close()
	log.Println("connected to PostgreSQL")

	// Прослушивание сервера
	users := repository.NewUserRepository(db)
	listingsService := service.NewListingService(repository.NewListingRepository(db))
	authService := service.NewAuthService(users, repository.NewSessionRepository(db), tokens)
	registration, err := service.NewRegistrationService(users, repository.NewVerificationRepository(db), mailer, cfg.Verification)
	if err != nil {
		return err
	}
	httpHandler := handler.New(listingsService, authService, registration)

	server := &http.Server{
		Addr:              cfg.HTTP.Address,
		Handler:           httpHandler.Routes(),
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		ReadHeaderTimeout: cfg.HTTP.ReadTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
	}

	shutdownSignal, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Periodically delete expired refresh sessions and email verifications.
	cleanupCtx, stopCleanup := context.WithCancel(context.Background())
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		service.RunCleanup(cleanupCtx, repository.NewCleanupRepository(db), cfg.Cleanup.Interval, cfg.Cleanup.Retention)
	}()
	// Runs before db.Close, so cleanup never uses a closed pool.
	defer func() {
		stopCleanup()
		<-cleanupDone
	}()
	serverErrors := make(chan error, 1)
	go func() {
		log.Printf("HTTP server listening on %s", server.Addr)
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve HTTP: %w", err)
		}
	case <-shutdownSignal.Done():
		stop()
		log.Println("shutting down HTTP server")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			server.Close()
			return fmt.Errorf("shutdown HTTP server: %w", err)
		}
	}

	return nil
}
