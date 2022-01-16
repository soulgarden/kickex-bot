package client

import (
	"sync"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/request"
	"github.com/tevino/abool"
)

func TestClient_sendMessage(t *testing.T) {
	t.Parallel()

	type fields struct {
		id       int64
		cfg      *conf.Bot
		conn     *websocket.Conn
		sendCh   chan request.Msg
		ReadCh   chan []byte
		logger   *zerolog.Logger
		isClosed *abool.AtomicBool
	}

	type args struct {
		payload []byte
	}

	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
	}{
		{
			name: "test 1",
			fields: fields{
				id:       0,
				cfg:      nil,
				conn:     &websocket.Conn{},
				sendCh:   make(chan request.Msg),
				ReadCh:   make(chan []byte),
				logger:   &zerolog.Logger{},
				isClosed: abool.New(),
			},
			args: args{
				payload: []byte(`{
					"id":"12342",
					"type":"createTradeOrder",
					"fields":{
						"pair":"KICK/USDT",
						"ordered_volume":"12313123",
						"limit_price":"10.3",
						"trade_intent":0
					},
					"externalId":"123e4567-e89b-12d3-a456-426614174000"
				}`),
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		tt := tt

		go func() {
			for range tt.fields.sendCh {
			}
		}()

		c := &Client{
			id:       tt.fields.id,
			cfg:      tt.fields.cfg,
			conn:     tt.fields.conn,
			sendCh:   tt.fields.sendCh,
			ReadCh:   tt.fields.ReadCh,
			logger:   tt.fields.logger,
			isClosed: tt.fields.isClosed,
		}

		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if err := c.sendMessage(tt.args.payload); (err != nil) != tt.wantErr {
				t.Errorf("sendMessage() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func BenchmarkClient_sendMessage(b *testing.B) {
	c := &Client{
		id:       0,
		cfg:      nil,
		conn:     &websocket.Conn{},
		sendCh:   make(chan request.Msg),
		ReadCh:   make(chan []byte),
		logger:   &zerolog.Logger{},
		isClosed: abool.New(),
	}

	go func() {
		for range c.sendCh {
		}
	}()

	payload := []byte(`{
					"id":"12342",
					"type":"createTradeOrder",
					"fields":{
						"pair":"KICK/USDT",
						"ordered_volume":"12313123",
						"limit_price":"10.3",
						"trade_intent":0
					},
					"externalId":"123e4567-e89b-12d3-a456-426614174000"
				}`)

	for i := 0; i < b.N; i++ {
		if err := c.sendMessage(payload); err != nil {
			b.Error(err)
			b.FailNow()
		}
	}
}

func BenchmarkClient_CreateOrder(b *testing.B) {
	c := &Client{
		id:       0,
		cfg:      nil,
		conn:     &websocket.Conn{},
		sendCh:   make(chan request.Msg),
		ReadCh:   make(chan []byte),
		logger:   &zerolog.Logger{},
		isClosed: abool.New(),
		createOrderPool: &sync.Pool{
			New: func() interface{} {
				return &request.CreateOrder{
					Fields: &request.CreateOrderFields{},
				}
			},
		},
		alterOrderPool: &sync.Pool{
			New: func() interface{} {
				return &request.AlterTradeOrder{
					CreateOrder: request.CreateOrder{
						Fields: &request.CreateOrderFields{},
					},
				}
			},
		},
		cancelOrderPool: &sync.Pool{
			New: func() interface{} {
				return &request.CancelOrder{}
			},
		},
	}

	go func() {
		for range c.sendCh {
		}
	}()

	for i := 0; i < b.N; i++ {
		oid, _, err := c.CreateOrder("KICK/ETH", "100000", "10.123", 0)
		if err != nil {
			b.Error(err)
			b.FailNow()
		}

		if i+1 != int(oid) {
			b.Fail()
		}
	}
}

func BenchmarkClient_AlterOrder(b *testing.B) {
	c := &Client{
		id:       0,
		cfg:      nil,
		conn:     &websocket.Conn{},
		sendCh:   make(chan request.Msg),
		ReadCh:   make(chan []byte),
		logger:   &zerolog.Logger{},
		isClosed: abool.New(),
		createOrderPool: &sync.Pool{
			New: func() interface{} {
				return &request.CreateOrder{
					Fields: &request.CreateOrderFields{},
				}
			},
		},
		alterOrderPool: &sync.Pool{
			New: func() interface{} {
				return &request.AlterTradeOrder{
					CreateOrder: request.CreateOrder{
						Fields: &request.CreateOrderFields{},
					},
				}
			},
		},
		cancelOrderPool: &sync.Pool{
			New: func() interface{} {
				return &request.CancelOrder{}
			},
		},
	}

	go func() {
		for range c.sendCh {
		}
	}()

	for i := 0; i < b.N; i++ {
		oid, _, err := c.AlterOrder("KICK/ETH", "100000", "10.123", 0, int64(i)+1)
		if err != nil {
			b.Error(err)
			b.FailNow()
		}

		if i+1 != int(oid) {
			b.Fail()
		}
	}
}

func BenchmarkClient_CancelOrder(b *testing.B) {
	c := &Client{
		id:       0,
		cfg:      nil,
		conn:     &websocket.Conn{},
		sendCh:   make(chan request.Msg),
		ReadCh:   make(chan []byte),
		logger:   &zerolog.Logger{},
		isClosed: abool.New(),
		createOrderPool: &sync.Pool{
			New: func() interface{} {
				return &request.CreateOrder{
					Fields: &request.CreateOrderFields{},
				}
			},
		},
		alterOrderPool: &sync.Pool{
			New: func() interface{} {
				return &request.AlterTradeOrder{
					CreateOrder: request.CreateOrder{
						Fields: &request.CreateOrderFields{},
					},
				}
			},
		},
		cancelOrderPool: &sync.Pool{
			New: func() interface{} {
				return &request.CancelOrder{}
			},
		},
	}

	go func() {
		for range c.sendCh {
		}
	}()

	for i := 0; i < b.N; i++ {
		oid, err := c.CancelOrder(1231231231231)
		if err != nil {
			b.Error(err)
			b.FailNow()
		}

		if i+1 != int(oid) {
			b.Fail()
		}
	}
}
