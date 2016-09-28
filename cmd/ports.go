package main

import (
	"sync"

	"github.com/uber-go/zap"
)

var (
	ports    map[uint]uint
	careful  sync.Mutex
	nextPort uint
)

func portPlz() uint {
	careful.Lock()

	if nextPort == 0 || nextPort >= 65535 {
		nextPort = PORT_RANGE_START
		log.Info("setting next port", zap.Uint("port", nextPort))
	}

	// TODO check whether next port is in the port map already
	p := nextPort
	nextPort++

	careful.Unlock()

	return p
}

func mapPorts(tor, privoxy uint) {
	careful.Lock()
	ports[tor] = privoxy
	ports[privoxy] = tor
	careful.Unlock()
}

func unmapPorts(tor, privoxy uint) {
	careful.Lock()
	delete(ports, tor)
	delete(ports, privoxy)
	careful.Unlock()
}
