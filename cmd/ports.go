package main

import (
	"sync"

	"go.uber.org/zap"
)

var (
	careful  sync.Mutex
	nextPort int
)

func portPlz() int {
	careful.Lock()

	if nextPort == 0 || nextPort >= 65535 {
		nextPort = *portRangeStart
		log.Info("setting next port", zap.Int("port", nextPort))
	}

	// TODO check whether next port is in the port map already
	p := nextPort
	nextPort++

	careful.Unlock()

	return p
}
