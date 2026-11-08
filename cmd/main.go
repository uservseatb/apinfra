package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/uservseatb/apinfra/internal/app"
	"github.com/uservseatb/apinfra/internal/sys"
)

const lockFileName = "apinfra.lock"

// Version is overridden at build time via -ldflags "-X main.Version=...".
//
//nolint:gochecknoglobals // overridden via -ldflags at build time
var Version = "dev+registry-login"

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("apinfra ")

	err := run()
	if err != nil {
		log.Fatalf("FATAL: %v", err)
	}
}

func run() error {
	lockPath, err := resolveLockPath()
	if err != nil {
		return fmt.Errorf("lock path: %w", err)
	}

	lock, err := sys.Acquire(lockPath)
	if err != nil {
		if errors.Is(err, sys.ErrAlreadyLocked) {
			log.Printf("skip: another apinfra process holds lock %s", lockPath)
			app.LogRunSeparator()

			return nil
		}

		return fmt.Errorf("lock: %w", err)
	}

	defer func() {
		releaseErr := lock.Release()
		if releaseErr != nil {
			log.Printf("WARN release lock: %v", releaseErr)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	err = app.RunUpdate(ctx, Version)
	if err != nil {
		return fmt.Errorf("run update: %w", err)
	}

	return nil
}

// resolveLockPath returns $HOME/apinfra.lock.
func resolveLockPath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}

	return filepath.Join(homeDir, lockFileName), nil
}
