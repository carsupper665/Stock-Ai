package model

import "server/utils"

var logger *utils.SysLogger

func InitDb() error {
	logger = utils.SysLog
	logger.Info("HI")
	return nil
}
