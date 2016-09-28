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

const CFG_TPL = `
user-manual /usr/share/doc/privoxy/user-manual/
confdir /etc/privoxy
logdir %s
actionsfile match-all.action # Actions that are applied to all sites and maybe overruled later on.
actionsfile default.action   # Main actions file
actionsfile user.action      # User customizations
filterfile default.filter
filterfile user.filter      # User customizations
logfile logfile
listen-address  127.0.0.1:%d
forward-socks5t / 127.0.0.1:%d .
toggle  1
enable-remote-toggle  0
enable-remote-http-toggle  0
enable-edit-actions 0
enforce-blocks 0
buffer-limit 4096
enable-proxy-authentication-forwarding 0
forwarded-connect-retries  0
accept-intercepted-requests 0
allow-cgi-request-crunching 0
split-large-forms 0
keep-alive-timeout 5
tolerate-pipelining 1
socket-timeout 300
`

type Privoxy struct {
	log  zap.Logger
	tor  *Tor
	cmd  *Cmd
	port uint
	dir  string
	pid  string
	conf string
}

func NewPrivoxy(ctx context.Context, tor *Tor) (p *Privoxy, err error) {
	p = &Privoxy{tor: tor}

	// loop until we find a port we like
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("application terminating")
		default:
		}

		p.port = portPlz()
		p.log = log.With(zap.String("service", "privoxy"),
			zap.Uint("port", p.port),
			zap.Uint("tor", tor.port))

		p.dir = fmt.Sprintf("/tmp/rotating-tor-proxy/privoxy-%d", p.port)
		p.pid = path.Join(p.dir, "privoxy.pid")
		p.conf = path.Join(p.dir, "privoxy.conf")

		if err = p.MakeDirs(); err != nil {
			p.log.Error("failed to write config", zap.Error(err))
			continue
		}

		p.cmd, err = NewCommand(ctx, p.log, "privoxy",
			"--no-daemon",
			"--pidfile", p.pid,
			p.conf)
		if err != nil {
			p.log.Error("failed to setup command", zap.Error(err))
			time.Sleep(500 * time.Millisecond)
			continue
		}

		p.cmd.transformLog = p.PrivoxyLogger

		break
	}

	return p, nil
}

func (p *Privoxy) MakeDirs() (err error) {
	if err = os.MkdirAll(p.dir, 0755); err != nil {
		return
	}

	var f *os.File
	if f, err = os.OpenFile(p.conf, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644); err != nil {
		return
	}
	defer f.Close()

	f.WriteString(fmt.Sprintf(CFG_TPL, p.dir, p.port, p.tor.port))

	return nil
}

func (p *Privoxy) PrivoxyLogger(line string) (level, msg string, fields []zap.Field) {
	line = line[37:]

	lvlPos := strings.Index(line, ":")
	level = strings.ToLower(line[:lvlPos])
	if strings.Contains(level, " ") {
		level = strings.Split(level, " ")[0]
	}

	msg = line[lvlPos+2:]

	return
}

func (p *Privoxy) Done() <-chan struct{} {
	return p.cmd.Done()
}

func (p *Privoxy) Wait() {
	p.cmd.Wait()
}

func (p *Privoxy) Close() (err error) {
	if p == nil {
		return nil
	}

	defer func() {
		if err = os.RemoveAll(p.dir); err != nil {
			p.log.Error("failed to data directory", zap.String("path", p.conf), zap.Error(err))
		}
	}()

	p.cmd.log.Info("cleaning up")
	if err = p.cmd.Close(); err != nil {
		if err.Error() != "signal: killed" {
			p.cmd.log.Error("failed to kill server", zap.Error(err))
		}
		return err
	}

	return nil
}
