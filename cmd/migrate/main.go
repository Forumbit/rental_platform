package main

import (
	"log"

	"github.com/forumbit/rental_platform/internal/config"
	"github.com/forumbit/rental_platform/migrations"
)

func main() {
	cfg := config.MustLoad()

	if err := migrations.RunMigrations(cfg.Database.URL); err != nil {
		log.Fatal(err)
	}

	log.Println("migrations applied")
}
