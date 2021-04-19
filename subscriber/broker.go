package subscriber

const eventChSize = 1024

type Broker struct {
	subscribers map[chan interface{}]struct{}
	subCh       chan chan interface{}
	unsubCh     chan chan interface{}
	publishCh   chan interface{}
}

func NewBroker() *Broker {
	return &Broker{
		subscribers: make(map[chan interface{}]struct{}),
		subCh:       make(chan chan interface{}, 1),
		unsubCh:     make(chan chan interface{}, 1),
		publishCh:   make(chan interface{}, eventChSize),
	}
}

func (b *Broker) Start() {
	for {
		select {
		case msgCh := <-b.subCh:
			b.subscribers[msgCh] = struct{}{}
		case msgCh := <-b.unsubCh:
			delete(b.subscribers, msgCh)
			close(msgCh)
		case msg := <-b.publishCh:
			for msgCh := range b.subscribers {
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
