package service

import (
	"time"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/conf"
	tb "gopkg.in/tucnak/telebot.v2"
)

const sendDelay = time.Millisecond * 500
const queueSize = 256

type Telegram struct {
	cfg    *conf.Bot
	logger *zerolog.Logger
	bot    *tb.Bot

	sendCh chan string
}

func NewTelegram(cfg *conf.Bot, logger *zerolog.Logger) (*Telegram, error) {
	bot, err := tb.NewBot(tb.Settings{
		Token: cfg.Telegram.Token,
	})

	if err != nil {
		logger.Err(err).Msg("new tg bot")

		return nil, err
	}

	return &Telegram{
		cfg:    cfg,
		logger: logger,
		sendCh: make(chan string, queueSize),
		bot:    bot,
	}, nil
}

func (s *Telegram) Start() {
	for msg := range s.sendCh {
		_ = s.send(msg)

		time.Sleep(sendDelay)
	}
}

func (s *Telegram) SendAsync(msg string) {
	s.sendCh <- msg
}

func (s *Telegram) SendSync(msg string) {
	_ = s.send(msg)
}

func (s *Telegram) send(msg string) error {
	_, err := s.bot.Send(&tb.Chat{ID: s.cfg.Telegram.ChatID}, msg)
	s.logger.Err(err).Str("msg", msg).Msg("send message")

	if err != nil {
		return err
	}

	return nil
}
