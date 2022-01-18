package spread

import (
	"context"
	"fmt"
	"os"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/service"
	"github.com/soulgarden/kickex-bot/storage"
)

type Dump struct {
	cfg      *conf.Bot
	orderSvc *service.Order
	logger   *zerolog.Logger
}

func NewDump(cfg *conf.Bot, orderSvc *service.Order, logger *zerolog.Logger) *Dump {
	return &Dump{cfg: cfg, orderSvc: orderSvc, logger: logger}
}

func (s *Dump) IsStateFileExists() (bool, error) {
	_, err := os.Stat(fmt.Sprintf(s.cfg.StorageDumpPath, s.cfg.Env))
	if err == nil {
		return true, nil
	}

	if os.IsNotExist(err) {
		return false, nil
	}

	s.logger.Err(err).Msg("open dump file")

	return false, err
}

func (s *Dump) LoadFromStateFile(ctx context.Context, st *storage.Storage) error {
	s.logger.Warn().Msg("load sessions from dump file")

	d := storage.NewDumpStorage(st)

	err := d.Recover(fmt.Sprintf(s.cfg.StorageDumpPath, s.cfg.Env))
	if err != nil {
		s.logger.Err(err).Msg("load sessions from dump")

		return err
	}

	err = os.Remove(fmt.Sprintf(s.cfg.StorageDumpPath, s.cfg.Env))
	if err != nil {
		s.logger.Err(err).Msg("remove dump file")

		return err
	}

	if len(st.GetUserOrders()) > 0 {
		err = s.orderSvc.UpdateOrdersStates(ctx)
		if err != nil {
			s.logger.Err(err).Msg("update orders for dumped state")

			return err
		}
	}

	return nil
}
