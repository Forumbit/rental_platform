package main

import (
	"context"
	"log"
	"time"

	"github.com/forumbit/rental_platform/internal/config"
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
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := storage.Open(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	users := service.NewUserService(repository.NewUserRepository(db))
	user, created, err := users.EnsureUser(ctx, cfg.SeedUser.Email, cfg.SeedUser.Password, cfg.SeedUser.FullName)
	if err != nil {
		return err
	}
	if created {
		log.Printf("user created: id=%d email=%s", user.ID, user.Email)
	} else {
		log.Printf("user already exists: id=%d email=%s", user.ID, user.Email)
	}
	return nil
}
