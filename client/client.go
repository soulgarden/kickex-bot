package client

import (
	"encoding/json"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/response"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/dictionary"

	"github.com/gorilla/websocket"
	"github.com/soulgarden/kickex-bot/request"
)

const pingInterval = 15 * time.Second
const readChSize = 256
const writeChSize = 1024
const readDeadline = 60 * time.Second
const eventSize = 8192

type Client struct {
	id     int64
	conn   *websocket.Conn
	sendCh chan []byte
	ReadCh chan []byte
	logger *zerolog.Logger
}

func (c *Client) read() {
	for {
		c.conn.SetReadLimit(eventSize)

		err := c.conn.SetReadDeadline(time.Now().Add(readDeadline))
		if err != nil {
			c.logger.Err(err).Msg("set read deadline")
		}

		c.conn.SetPongHandler(func(string) error {
			err = c.conn.SetReadDeadline(time.Now().Add(readDeadline))
			if err != nil {
				c.logger.Err(err).Msg("set read deadline")
			}

			return nil
		})

		_, sourceMessage, err := c.conn.ReadMessage()
		c.logger.Debug().
			Bytes("payload", sourceMessage).
			Msg("Got message")

		if err != nil {
			if websocket.IsUnexpectedCloseError(
				err,
				websocket.CloseGoingAway,
				websocket.CloseNormalClosure,
				websocket.CloseAbnormalClosure,
			) {
				c.logger.Warn().Err(err).Msg("Unexpected close error")
			}

			break
		}

		c.ReadCh <- sourceMessage
	}
}

func (c *Client) write() {
	for {
		message, ok := <-c.sendCh

		if !ok {
			return
		}

		err := c.conn.WriteMessage(websocket.TextMessage, message)
		if websocket.IsUnexpectedCloseError(
			err,
			websocket.CloseGoingAway,
			websocket.CloseNormalClosure,
			websocket.CloseAbnormalClosure,
		) {
			c.logger.Warn().Err(err).Msg("Unexpected close error")
		}
	}
}

func (c *Client) pinger() {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()

	for range ticker.C {
		if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
			if websocket.IsUnexpectedCloseError(
				err,
				websocket.CloseGoingAway,
				websocket.CloseNormalClosure,
				websocket.CloseAbnormalClosure,
			) {
				c.logger.Warn().Err(err).Msg("Unexpected close error")
			}

			return
		}
	}
}

func (c *Client) Close() {
	err := c.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	if websocket.IsUnexpectedCloseError(
		err,
		websocket.CloseGoingAway,
		websocket.CloseNormalClosure,
		websocket.CloseAbnormalClosure,
	) {
		c.logger.Warn().Err(err).Msg("Unexpected close error")
	}
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

func (c *Client) sendMessage(payload interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	c.logger.Debug().Bytes("body", body).Msg("send")

	c.sendCh <- body

	return nil
}

func newConnection(url string, logger *zerolog.Logger) (*Client, error) {
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		logger.Err(err).Msg("Dial error")

		return nil, err
	}

	logger.Debug().Msg("New connection established")

	cli := &Client{
		conn:   conn,
		sendCh: make(chan []byte, writeChSize),
		ReadCh: make(chan []byte, readChSize),
		logger: logger,
	}

	return cli, err
}

func NewWsCli(cfg *conf.Bot, logger *zerolog.Logger) (*Client, error) {
	cli, err := newConnection(
		(&url.URL{Scheme: cfg.Scheme, Host: cfg.DefaultAddr, Path: "/ws"}).String(),
		logger,
	)
	if err != nil {
		logger.Err(err).Msg("connection error")

		return nil, err
	}

	go cli.read()
	go cli.write()
	go cli.pinger()

	err = cli.authorize(cfg.APIKey, cfg.APIKeyPass)
	if err != nil {
		logger.Err(err).Msg("authorization")

		return nil, err
	}

	msg := <-cli.ReadCh

	logger.Debug().
		Bytes("payload", msg).
		Msg("got auth message")

	er := &response.Error{}

	err = json.Unmarshal(msg, er)
	if err != nil {
		logger.Fatal().Err(err).Msg("unmarshall")
	}

	if er.Error != nil {
		logger.Fatal().Err(err).Msg("auth error")
	}

	return cli, nil
}
