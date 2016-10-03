package main

import (
	"sync"

	"github.com/uber-go/zap"
)

var (
	ports    map[int]int
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

func mapPorts(tor, privoxy int) {
	careful.Lock()
	ports[tor] = privoxy
	ports[privoxy] = tor
	careful.Unlock()
}

func unmapPorts(tor, privoxy int) {
	careful.Lock()
	delete(ports, tor)
	delete(ports, privoxy)
	careful.Unlock()
}
