package main

import (
	"context"
	"os"
	"path"
	"strings"
	"text/template"

	"github.com/uber-go/zap"
)

const HAPROXY_TPL = `
global
  maxconn 1024
  pidfile {{.PidFile}}

defaults
  mode http
  maxconn 1024
  option  httplog
  option  dontlognull
  retries 3
  timeout connect 5s
  timeout client 60s
  timeout server 60s


listen stats *:{{.StatsPort}}
  mode            http
  log             global
  maxconn 10
  clitimeout      100s
  srvtimeout      100s
  contimeout      100s
  timeout queue   100s
  stats enable
  stats hide-version
  stats refresh 30s
  stats show-node
  stats uri /haproxy?stats


frontend rotating_proxies
  bind *:{{.Port}}
  default_backend tor
  option http_proxy

backend tor
  option http_proxy
  balance leastconn # http://cbonte.github.io/haproxy-dconv/configuration-1.5.html#balance

  {{ range $port, $be := .Backends }}
  server privoxy-{{ $port }} 127.0.0.1:{{ $port }}
  {{ end }}
`

type HAProxy struct {
	log zap.Logger
	cmd *Cmd

	dir  string
	conf string

	PidFile   string
	Port      uint
	StatsPort uint
	Backends  map[uint]struct{}
}

func NewHAProxy(ctx context.Context, port uint) (h *HAProxy, err error) {
	h = &HAProxy{
		log: log.With(zap.String("service", "haproxy"), zap.Uint("port", port)),
		dir: "/tmp/torotator/haproxy",

		Port:      port,
		StatsPort: 1936,
		Backends:  make(map[uint]struct{}),
	}

	h.conf = path.Join(h.dir, "haproxy.cfg")
	h.PidFile = path.Join(h.dir, "haproxy.pid")

	if err = h.MakeDirs(); err != nil {
		h.log.Error("failed to write config", zap.Error(err))
		return nil, err
	}

	h.cmd, err = NewCommand(ctx, h.log, "haproxy", "-f", h.conf)
	if err != nil {
		h.log.Error("failed to setup command", zap.Error(err))
		return nil, err
	}

	h.cmd.transformLog = h.HAProxyLogger

	return h, nil
}

func (h *HAProxy) MakeDirs() (err error) {
	if err = os.MkdirAll(h.dir, 0755); err != nil {
		return
	}

	if err = h.WriteConfig(); err != nil {
		return
	}

	return nil
}

func (h *HAProxy) HAProxyLogger(line string) (level, msg string, fields []zap.Field) {
	line = line[1:]

	lvlPos := strings.Index(line, "]")
	level = strings.ToLower(line[:lvlPos])
	switch level {
	case "alert":
		level = "error"
	case "warning":
		level = "warn"
	default:
		h.log.Debug("noticed unmapped log level", zap.String("name", level))
	}

	line = line[lvlPos:]
	msgPos := strings.Index(line, ":")
	msg = line[msgPos+2:]

	return
}

func (h *HAProxy) WriteConfig() (err error) {
	var f *os.File

	if f, err = os.OpenFile(h.conf, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644); err != nil {
		return
	}
	defer f.Close()

	t := template.New("haproxy")
	if t, err = t.Parse(HAPROXY_TPL); err != nil {
		h.log.Error("unable to parse template", zap.Error(err))
		return
	}

	if err = t.Execute(f, h); err != nil {
		h.log.Error("unable to render template", zap.Error(err))
		return
	}

	return nil
}

func (h *HAProxy) AddBackend(port uint) {
	h.Backends[port] = struct{}{}

	h.WriteConfig()
}

func (h *HAProxy) RemoveBackend(port uint) {
	delete(h.Backends, port)

	h.WriteConfig()
}

func (h *HAProxy) Done() <-chan struct{} {
	return h.cmd.Done()
}

func (h *HAProxy) Wait() {
	h.cmd.Wait()
}

func (h *HAProxy) Close() (err error) {
	if h == nil {
		return nil
	}

	defer func() {
		if err = os.RemoveAll(h.dir); err != nil {
			h.log.Error("failed to data directory", zap.String("path", h.dir), zap.Error(err))
		}
	}()

	h.cmd.log.Info("cleaning up")
	if err = h.cmd.Close(); err != nil {
		if err.Error() != "signal: killed" {
			h.cmd.log.Error("failed to kill server", zap.Error(err))
		}
		return err
	}

	return nil
}
