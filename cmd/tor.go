package main

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/uber-go/zap"
)

type Tor struct {
	log  zap.Logger
	cmd  *Cmd
	port uint
	dir  string
	pid  string
}

func NewTor(ctx context.Context) (t *Tor, err error) {
	t = &Tor{}

	// loop until we find a port we like
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("application terminating")
		default:
		}

		t.port = portPlz()
		t.log = log.With(zap.String("service", "tor"), zap.Uint("port", t.port))
		t.dir = fmt.Sprintf("/tmp/rotating-tor-proxy/tor-%d", t.port)
		t.pid = path.Join(t.dir, "tor.pid")

		t.MakeDirs()

		t.cmd, err = NewCommand(ctx, t.log, "tor",
			"--allow-missing-torrc",
			"--SocksPort", fmt.Sprintf("%d", t.port),
			"--NewCircuitPeriod", "120",
			"--DataDirectory", t.dir,
			"--PidFile", t.pid,
			"--Log", "warn stdout")
		if err != nil {
			t.log.Error("failed to setup command", zap.Error(err))
			time.Sleep(500 * time.Millisecond)
			continue
		}

		t.cmd.transformLog = t.TorLogger

		break
	}

	return t, nil
}

func (t *Tor) MakeDirs() (err error) {
	if err = os.MkdirAll(t.dir, 0700); err != nil {
		return
	}

	return nil
}

func (t *Tor) TorLogger(line string) (level, msg string, fields []zap.Field) {
	line = line[21:]
	lvlPos := strings.Index(line, "]")
	level = line[:lvlPos]
	msg = line[lvlPos+2:]

	return
}

func (t *Tor) Done() <-chan struct{} {
	return t.cmd.Done()
}

func (t *Tor) Wait() {
	t.cmd.Wait()
}

func (t *Tor) Close() (err error) {
	if t == nil {
		return nil
	}

	defer func() {
		if err = os.RemoveAll(t.dir); err != nil {
			t.log.Error("failed to remove data directory", zap.String("path", t.dir), zap.Error(err))
		}
	}()

	t.cmd.log.Info("cleaning up")
	if err = t.cmd.Close(); err != nil {
		if err.Error() != "signal: killed" {
			t.cmd.log.Error("failed to kill server", zap.Error(err))
		}
		return
	}

	return nil
}
