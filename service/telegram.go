package service

import (
	"time"

	"github.com/soulgarden/kickex-bot/dictionary"

	"github.com/soulgarden/kickex-bot/conf"

	"github.com/rs/zerolog"
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

func NewTelegram(cfg *conf.Bot, bot *tb.Bot, logger *zerolog.Logger) *Telegram {
	return &Telegram{
		cfg:    cfg,
		logger: logger,
		sendCh: make(chan string, queueSize),
		bot:    bot,
	}
}

func (s *Telegram) Start() {
	for msg := range s.sendCh {
		_ = s.send(msg)

		time.Sleep(sendDelay)
	}
}

func (s *Telegram) SendAsync(msg string) {
	if len(s.sendCh) == queueSize {
		s.logger.
			Err(dictionary.ErrChannelOverflowed).
			Interface("msg", msg).
			Msg(dictionary.ErrChannelOverflowed.Error())

		return
	}

	s.sendCh <- msg
}

func (s *Telegram) SendSync(msg string) {
	_ = s.send(msg)
}

func (s *Telegram) send(msg string) error {
	_, err := s.bot.Send(&tb.Chat{ID: s.cfg.Telegram.ChatID}, msg)
	if err != nil {
		s.logger.Err(err).Str("msg", msg).Msg("send message")

		return err
	}

	return nil
}
