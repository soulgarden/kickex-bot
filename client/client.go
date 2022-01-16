package client

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/mailru/easyjson"

	uuid "github.com/satori/go.uuid"

	"github.com/soulgarden/kickex-bot/response"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/dictionary"

	"github.com/gorilla/websocket"
	"github.com/soulgarden/kickex-bot/request"
	"github.com/tevino/abool"
)

const pingInterval = 15 * time.Second
const readChSize = 1024
const writeChSize = 1024
const readDeadline = 20 * time.Second
const eventSize = 32768

type Client struct {
	id              int64
	cfg             *conf.Bot
	conn            *websocket.Conn
	sendCh          chan request.Msg
	ReadCh          chan []byte
	logger          *zerolog.Logger
	isClosed        *abool.AtomicBool
	createOrderPool *sync.Pool
	alterOrderPool  *sync.Pool
	cancelOrderPool *sync.Pool
}

func (c *Client) read(interrupt chan<- os.Signal) {
	for {
		c.conn.SetReadLimit(eventSize)

		err := c.conn.SetReadDeadline(time.Now().Add(readDeadline))
		if err != nil {
			c.logger.Err(err).Msg("set read deadline")

			interrupt <- syscall.SIGINT
		}

		c.conn.SetPongHandler(func(string) error {
			err = c.conn.SetReadDeadline(time.Now().Add(readDeadline))
			if err != nil {
				c.logger.Err(err).Msg("set read deadline")

				interrupt <- syscall.SIGINT
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

				interrupt <- syscall.SIGINT
			} else if !websocket.IsCloseError(
				err,
				websocket.CloseGoingAway,
				websocket.CloseNormalClosure,
			) {
				c.logger.Err(err).Msg("got error")

				interrupt <- syscall.SIGINT
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

func (c *Client) write(interrupt chan<- os.Signal) {
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

				interrupt <- syscall.SIGINT
			}
		}
	}
}

func (c *Client) pinger() {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()

	for range ticker.C {
		if c.isClosed.IsNotSet() {
			c.sendCh <- request.Msg{Type: websocket.PingMessage}
		}
	}
}

func (c *Client) Close() {
	if c.isClosed.IsSet() {
		return
	}

	c.isClosed.Set()

	c.sendCh <- request.Msg{
		Type:    websocket.CloseMessage,
		Payload: websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
	}

	close(c.sendCh)
	close(c.ReadCh)
}

func (c *Client) authorize(apiKey, apiKeyPass string) error {
	id := atomic.AddInt64(&c.id, 1)

	r := &request.Auth{
		ID:       strconv.FormatInt(id, dictionary.DefaultIntBase),
		Type:     dictionary.AuthType,
		APIKey:   apiKey,
		Password: apiKeyPass,
	}

	body, err := easyjson.Marshal(r)
	if err != nil {
		return err
	}

	return c.sendMessage(body)
}

// nolint: unused
func (c *Client) getUserOpenOrders(pair string) (int64, error) {
	id := atomic.AddInt64(&c.id, 1)

	r := &request.GetUsersOpenOrders{
		ID:   strconv.FormatInt(id, dictionary.DefaultIntBase),
		Type: dictionary.GetUsersOpenOrders,
		Pair: pair,
	}

	body, err := easyjson.Marshal(r)
	if err != nil {
		return id, err
	}

	return id, c.sendMessage(body)
}

func (c *Client) SubscribeAccounting(includeDeals bool) (int64, error) {
	id := atomic.AddInt64(&c.id, 1)

	r := &request.SubscribeAccounting{
		ID:           strconv.FormatInt(id, dictionary.DefaultIntBase),
		Type:         dictionary.SubscribeAccounting,
		IncludeDeals: includeDeals,
	}

	body, err := easyjson.Marshal(r)
	if err != nil {
		return id, err
	}

	return id, c.sendMessage(body)
}

func (c *Client) GetOrderBookAndSubscribe(pairs string) (int64, error) {
	id := atomic.AddInt64(&c.id, 1)

	r := &request.GetOrderBookAndSubscribe{
		ID:   strconv.FormatInt(id, dictionary.DefaultIntBase),
		Type: dictionary.GetOrderBookAndSubscribe,
		Pair: pairs,
	}

	body, err := easyjson.Marshal(r)
	if err != nil {
		return id, err
	}

	return id, c.sendMessage(body)
}

func (c *Client) GetPairsAndSubscribe() (int64, error) {
	id := atomic.AddInt64(&c.id, 1)

	r := &request.GetPairsAndSubscribe{
		ID:   strconv.FormatInt(id, dictionary.DefaultIntBase),
		Type: dictionary.GetPairsAndSubscribe,
	}

	body, err := easyjson.Marshal(r)
	if err != nil {
		return id, err
	}

	return id, c.sendMessage(body)
}

func (c *Client) CreateOrder(pair, volume, limitPrice string, tradeIntent int) (int64, string, error) {
	id := atomic.AddInt64(&c.id, 1)

	r, ok := c.createOrderPool.Get().(*request.CreateOrder)
	if !ok {
		return id, "", dictionary.ErrInterfaceAssertion
	}

	defer c.createOrderPool.Put(r)

	extID := uuid.NewV4().String()

	r.ID = strconv.FormatInt(id, dictionary.DefaultIntBase)
	r.Type = dictionary.CreateTradeOrder
	r.Fields.Pair = pair
	r.Fields.OrderedVolume = volume
	r.Fields.LimitPrice = limitPrice
	r.Fields.TradeIntent = tradeIntent

	r.ExternalID = extID

	body, err := easyjson.Marshal(r)
	if err != nil {
		return id, extID, err
	}

	return id, extID, c.sendMessage(body)
}

func (c *Client) AlterOrder(pair, volume, limitPrice string, tradeIntent int, orderID int64) (int64, string, error) {
	id := atomic.AddInt64(&c.id, 1)

	r, ok := c.alterOrderPool.Get().(*request.AlterTradeOrder)
	if !ok {
		return id, "", dictionary.ErrInterfaceAssertion
	}

	defer c.alterOrderPool.Put(r)

	extID := uuid.NewV4().String()

	r.CreateOrder.ID = strconv.FormatInt(id, dictionary.DefaultIntBase)
	r.CreateOrder.Type = dictionary.AlterTradeOrder
	r.CreateOrder.Fields.Pair = pair
	r.CreateOrder.Fields.OrderedVolume = volume
	r.CreateOrder.Fields.LimitPrice = limitPrice
	r.CreateOrder.Fields.TradeIntent = tradeIntent
	r.ExternalID = extID
	r.OrderID = orderID

	body, err := easyjson.Marshal(r)
	if err != nil {
		return id, extID, err
	}

	return id, extID, c.sendMessage(body)
}

func (c *Client) CancelOrder(orderID int64) (int64, error) {
	id := atomic.AddInt64(&c.id, 1)

	r, ok := c.cancelOrderPool.Get().(*request.CancelOrder)
	if !ok {
		return id, dictionary.ErrInterfaceAssertion
	}

	defer c.cancelOrderPool.Put(r)

	r.ID = strconv.FormatInt(id, dictionary.DefaultIntBase)
	r.Type = dictionary.CancelOrder
	r.OrderID = orderID

	body, err := easyjson.Marshal(r)
	if err != nil {
		return id, err
	}

	return id, c.sendMessage(body)
}

func (c *Client) GetOrder(orderID int64) (int64, error) {
	id := atomic.AddInt64(&c.id, 1)

	r := &request.GetOrder{
		ID:      strconv.FormatInt(id, dictionary.DefaultIntBase),
		Type:    dictionary.GetOrder,
		OrderID: orderID,
	}

	body, err := easyjson.Marshal(r)
	if err != nil {
		return id, err
	}

	return id, c.sendMessage(body)
}

func (c *Client) GetOrderByExtID(extID string) (int64, error) {
	id := atomic.AddInt64(&c.id, 1)

	r := &request.GetOrder{
		ID:         strconv.FormatInt(id, dictionary.DefaultIntBase),
		Type:       dictionary.GetOrder,
		ExternalID: extID,
	}

	body, err := easyjson.Marshal(r)
	if err != nil {
		return id, err
	}

	return id, c.sendMessage(body)
}

func (c *Client) GetBalance() (int64, error) {
	id := atomic.AddInt64(&c.id, 1)

	r := &request.GetBalance{
		ID:   strconv.FormatInt(id, dictionary.DefaultIntBase),
		Type: dictionary.GetBalance,
	}

	body, err := easyjson.Marshal(r)
	if err != nil {
		return id, err
	}

	return id, c.sendMessage(body)
}

func (c *Client) sendMessage(body []byte) error {
	c.logger.Debug().Int("type", websocket.TextMessage).Bytes("body", body).Msg("send message")

	if c.isClosed.IsNotSet() {
		c.sendCh <- request.Msg{Type: websocket.TextMessage, Payload: body}
	} else {
		c.logger.Warn().
			Int("type", websocket.TextMessage).
			Bytes("body", body).
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

	err = easyjson.Unmarshal(msg, er)
	if err != nil {
		c.logger.Err(err).Msg("unmarshall")

		return err
	}

	if er.Error != nil {
		err = fmt.Errorf("%w: %s", dictionary.ErrResponse, er.Error.Reason)
		c.logger.Err(err).Bytes("response", msg).Msg("auth error")

		return err
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
		sendCh:   make(chan request.Msg, writeChSize),
		ReadCh:   make(chan []byte, readChSize),
		logger:   logger,
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

	return cli, err
}

func NewWsCli(cfg *conf.Bot, interrupt chan<- os.Signal, logger *zerolog.Logger) (*Client, error) {
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
