package broker

import (
	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/dictionary"
)

const eventChSize = 1024

type Broker struct {
	subscribers map[chan interface{}]struct{}
	subCh       chan chan interface{}
	unsubCh     chan chan interface{}
	publishCh   chan interface{}
	logger      *zerolog.Logger
}

func New(logger *zerolog.Logger) *Broker {
	return &Broker{
		subscribers: make(map[chan interface{}]struct{}),
		subCh:       make(chan chan interface{}, 1),
		unsubCh:     make(chan chan interface{}, 1),
		publishCh:   make(chan interface{}, eventChSize),
		logger:      logger,
	}
}

func (b *Broker) Start() {
	for {
		select {
		case msgCh := <-b.subCh:
			b.subscribers[msgCh] = struct{}{}
		case msgCh := <-b.unsubCh:
			if _, ok := b.subscribers[msgCh]; !ok {
				continue
			}

			delete(b.subscribers, msgCh)
			close(msgCh)
		case msg := <-b.publishCh:
			for msgCh := range b.subscribers {
				if len(msgCh) == eventChSize {
					b.logger.Err(dictionary.ErrChannelOverflowed).Msg(dictionary.ErrChannelOverflowed.Error())

					continue
				}

				msgCh <- msg
			}
		}
	}
}

func (b *Broker) Subscribe() chan interface{} {
	msgCh := make(chan interface{}, eventChSize)
	b.subCh <- msgCh

	return msgCh
}

func (b *Broker) Unsubscribe(msgCh chan interface{}) {
	b.unsubCh <- msgCh
}

func (b *Broker) Publish(msg interface{}) {
	b.publishCh <- msg
}
