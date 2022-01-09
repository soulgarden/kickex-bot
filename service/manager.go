package service

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/dictionary"
)

type Manager struct {
	logger *zerolog.Logger
}

func NewManager(logger *zerolog.Logger) *Manager {
	return &Manager{logger: logger}
}

func (s *Manager) ListenSignal() (context.Context, chan<- os.Signal) {
	interrupt := make(chan os.Signal, dictionary.SignalChLen)

	signal.Notify(interrupt, os.Interrupt)
	signal.Notify(interrupt, syscall.SIGTERM)

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		<-interrupt

		s.logger.Warn().Msg("interrupt signal received")

		cancel()

		<-time.After(dictionary.ShutDownDuration)

		s.logger.Warn().Msg("killed by shutdown timeout")

		os.Exit(1)
	}()

	go func() {
		<-ctx.Done()

		s.logger.Debug().Msg("start graceful shutting down")
	}()

	return ctx, interrupt
}
