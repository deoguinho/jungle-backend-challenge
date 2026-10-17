package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("migration command failed", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return errors.New("usage: migrate <up|down> [steps]")
	}
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	if _, err := os.Stat("migrations"); err != nil {
		return fmt.Errorf("migrations directory unavailable: %w", err)
	}

	m, err := migrate.New("file://migrations", databaseURL)
	if err != nil {
		return fmt.Errorf("initialize migrations: %w", err)
	}
	defer func() {
		if sourceErr, dbErr := m.Close(); sourceErr != nil || dbErr != nil {
			slog.Warn("close migration resources", "source_error", sourceErr, "database_error", dbErr)
		}
	}()

	switch args[0] {
	case "up":
		if len(args) != 1 {
			return errors.New("usage: migrate up")
		}
		err = m.Up()
	case "down":
		steps := 1
		if len(args) == 2 {
			if _, scanErr := fmt.Sscanf(args[1], "%d", &steps); scanErr != nil || steps < 1 {
				return fmt.Errorf("invalid migration down step count %q", args[1])
			}
		}
		err = m.Steps(-steps)
	default:
		return errors.New("migration direction must be up or down")
	}
	if errors.Is(err, migrate.ErrNoChange) {
		slog.Info("database schema is already at the requested version")
		return nil
	}
	if err != nil {
		return fmt.Errorf("apply %s migrations: %w", args[0], err)
	}
	slog.Info("database migrations applied", "direction", args[0])
	return nil
}
