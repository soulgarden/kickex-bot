package client

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/soulgarden/kickex-bot/response"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/dictionary"

	"github.com/gorilla/websocket"
	"github.com/soulgarden/kickex-bot/request"
	"github.com/tevino/abool"
)

const pingInterval = 15 * time.Second
const readChSize = 256
const writeChSize = 1024
const readDeadline = 20 * time.Second
const eventSize = 32768

type Client struct {
	id       int64
	cfg      *conf.Bot
	conn     *websocket.Conn
	sendCh   chan Msg
	ReadCh   chan []byte
	logger   *zerolog.Logger
	isClosed *abool.AtomicBool
}

type Msg struct {
	Type    int
	Payload []byte
}

func (c *Client) read(interrupt chan os.Signal) {
	for {
		c.conn.SetReadLimit(eventSize)

		err := c.conn.SetReadDeadline(time.Now().Add(readDeadline))
		if err != nil {
			c.logger.Err(err).Msg("set read deadline")

			interrupt <- syscall.SIGSTOP
		}

		c.conn.SetPongHandler(func(string) error {
			err = c.conn.SetReadDeadline(time.Now().Add(readDeadline))
			if err != nil {
				c.logger.Err(err).Msg("set read deadline")

				interrupt <- syscall.SIGSTOP
			}

			return nil
		})

		msgType, sourceMessage, err := c.conn.ReadMessage()
		c.logger.Debug().
			Int("type", msgType).
			Bytes("payload", sourceMessage).
			Msg("got message")

		if err != nil {
			if websocket.IsUnexpectedCloseError(
				err,
				websocket.CloseGoingAway,
				websocket.CloseNormalClosure,
			) {
				c.logger.Warn().Err(err).Msg("unexpected close error")

				interrupt <- syscall.SIGSTOP
			} else if !websocket.IsCloseError(
				err,
				websocket.CloseGoingAway,
				websocket.CloseNormalClosure,
			) {
				c.logger.Err(err).Msg("got error")

				interrupt <- syscall.SIGSTOP
			}

			return
		}

		if c.isClosed.IsNotSet() {
			c.ReadCh <- sourceMessage
		} else {
			c.logger.Warn().Bytes("payload", sourceMessage).Msg("got message, but read channel closed")
		}
	}
}

func (c *Client) write(interrupt chan os.Signal) {
	for {
		msg, ok := <-c.sendCh

		if !ok {
			return
		}

		err := c.conn.WriteMessage(msg.Type, msg.Payload)
		if err != nil {
			if websocket.IsUnexpectedCloseError(
				err,
				websocket.CloseGoingAway,
				websocket.CloseNormalClosure,
			) {
				c.logger.Warn().Err(err).Msg("Unexpected close error")
			}

			if c.isClosed.IsNotSet() && msg.Type == websocket.PingMessage {
				c.logger.Err(err).
					Int("type", msg.Type).
					Bytes("body", msg.Payload).
					Msg("ping failed, interrupt")
				interrupt <- syscall.SIGSTOP
			}
		}
	}
}

func (c *Client) pinger() {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()

	for range ticker.C {
		if c.isClosed.IsNotSet() {
			c.sendCh <- Msg{Type: websocket.PingMessage}
		}
	}
}

func (c *Client) Close() {
	if c.isClosed.IsSet() {
		return
	}

	c.isClosed.Set()

	c.sendCh <- Msg{Type: websocket.CloseMessage, Payload: websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")}

	close(c.sendCh)
	close(c.ReadCh)
}

func (c *Client) authorize(apiKey, apiKeyPass string) error {
	atomic.AddInt64(&c.id, 1)

	body := &request.Auth{
		ID:       strconv.FormatInt(c.id, 10),
		Type:     dictionary.AuthType,
		APIKey:   apiKey,
		Password: apiKeyPass,
	}

	return c.sendMessage(body)
}

// nolint: unused
func (c *Client) getUserOpenOrders(pair string) error {
	atomic.AddInt64(&c.id, 1)

	body := &request.GetUsersOpenOrders{
		ID:   strconv.FormatInt(c.id, 10),
		Type: dictionary.GetUsersOpenOrders,
		Pair: pair,
	}

	return c.sendMessage(body)
}

func (c *Client) SubscribeAccounting(includeDeals bool) error {
	atomic.AddInt64(&c.id, 1)

	body := &request.SubscribeAccounting{
		ID:           strconv.FormatInt(c.id, 10),
		Type:         dictionary.SubscribeAccounting,
		IncludeDeals: includeDeals,
	}

	return c.sendMessage(body)
}

func (c *Client) GetOrderBookAndSubscribe(pairs string) error {
	atomic.AddInt64(&c.id, 1)

	body := &request.GetOrderBookAndSubscribe{
		ID:   strconv.FormatInt(c.id, 10),
		Type: dictionary.GetOrderBookAndSubscribe,
		Pair: pairs,
	}

	return c.sendMessage(body)
}

func (c *Client) GetPairsAndSubscribe() error {
	atomic.AddInt64(&c.id, 1)

	body := &request.GetPairsAndSubscribe{
		ID:   strconv.FormatInt(c.id, 10),
		Type: dictionary.GetPairsAndSubscribe,
	}

	return c.sendMessage(body)
}

func (c *Client) CreateOrder(pair, volume, limitPrice string, tradeIntent int) (int64, error) {
	atomic.AddInt64(&c.id, 1)

	body := &request.CreateOrder{
		ID:   strconv.FormatInt(c.id, 10),
		Type: dictionary.CreateTradeOrder,
		Fields: &request.CreateOrderFields{
			Pair:          pair,
			OrderedVolume: volume,
			LimitPrice:    limitPrice,
			TradeIntent:   tradeIntent,
		},
		ExternalID: strconv.FormatInt(c.id, 10),
	}

	return c.id, c.sendMessage(body)
}

// nolint: unused
func (c *Client) alterOrder(pair, volume, limitPrice string, tradeIntent int, orderID int64) (int64, error) {
	atomic.AddInt64(&c.id, 1)

	body := &request.AlterTradeOrder{
		CreateOrder: request.CreateOrder{
			ID:   strconv.FormatInt(c.id, 10),
			Type: dictionary.CreateTradeOrder,
			Fields: &request.CreateOrderFields{
				Pair:          pair,
				OrderedVolume: volume,
				LimitPrice:    limitPrice,
				TradeIntent:   tradeIntent,
			},
			ExternalID: strconv.FormatInt(c.id, 10),
		},
		OrderID: orderID,
	}

	return c.id, c.sendMessage(body)
}

func (c *Client) CancelOrder(orderID int64) error {
	atomic.AddInt64(&c.id, 1)

	body := &request.CancelOrder{
		ID:      strconv.FormatInt(c.id, 10),
		Type:    dictionary.CancelOrder,
		OrderID: orderID,
	}

	return c.sendMessage(body)
}

func (c *Client) GetOrder(orderID int64) error {
	atomic.AddInt64(&c.id, 1)

	body := &request.GetOrder{
		ID:      strconv.FormatInt(c.id, 10),
		Type:    dictionary.GetOrder,
		OrderID: orderID,
	}

	return c.sendMessage(body)
}

func (c *Client) sendMessage(payload interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	c.logger.Debug().Int("type", websocket.TextMessage).Bytes("body", body).Msg("send")

	if c.isClosed.IsNotSet() {
		c.sendCh <- Msg{Type: websocket.TextMessage, Payload: body}
	} else {
		c.logger.Warn().
			Int("type", websocket.TextMessage).
			Interface("body", payload).
			Msg("got message fot sent, but write channel closed")
	}

	return nil
}

func (c *Client) Auth() error {
	err := c.authorize(c.cfg.APIKey, c.cfg.APIKeyPass)
	if err != nil {
		c.logger.Err(err).Msg("authorization")

		return nil
	}

	msg, ok := <-c.ReadCh
	if !ok {
		return dictionary.ErrWsReadChannelClosed
	}

	c.logger.Debug().
		Bytes("body", msg).
		Msg("got auth message")

	er := &response.Error{}

	err = json.Unmarshal(msg, er)
	if err != nil {
		c.logger.Err(err).Msg("unmarshall")

		return err
	}

	if er.Error != nil {
		c.logger.Err(err).Bytes("payload", msg).Msg("auth error")

		return errors.New(er.Error.Reason)
	}

	return nil
}

func newConnection(cfg *conf.Bot, logger *zerolog.Logger) (*Client, error) {
	conn, _, err := websocket.DefaultDialer.Dial(
		(&url.URL{Scheme: cfg.Scheme, Host: cfg.DefaultAddr, Path: "/ws"}).String(),
		nil,
	)
	if err != nil {
		logger.Err(err).Msg("Dial error")

		return nil, err
	}

	logger.Debug().Msg("New connection established")

	cli := &Client{
		cfg:      cfg,
		conn:     conn,
		sendCh:   make(chan Msg, writeChSize),
		ReadCh:   make(chan []byte, readChSize),
		logger:   logger,
		isClosed: abool.New(),
	}

	return cli, err
}

func NewWsCli(cfg *conf.Bot, interrupt chan os.Signal, logger *zerolog.Logger) (*Client, error) {
	cli, err := newConnection(
		cfg,
		logger,
	)
	if err != nil {
		logger.Err(err).Msg("connection error")

		return nil, err
	}

	go cli.read(interrupt)
	go cli.write(interrupt)
	go cli.pinger()

	return cli, nil
}
