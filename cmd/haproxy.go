package main

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/uber-go/zap"
)

const HAPROXY_TPL = `
global
  maxconn {{.MaxConn}}

defaults
  mode http
  maxconn 1024
  option  httplog
  option  dontlognull
  retries 3
  timeout connect 5s
  timeout client  30s
  timeout server  30s

{{ if .EnableStats }}
listen stats
  bind            :{{.StatsPort}}
  mode            http
  maxconn 10
  timeout client  100s
  timeout server  100s
  timeout connect 100s
  timeout queue   100s
  stats enable
  stats hide-version
  stats refresh 30s
  stats show-node
  stats uri /haproxy?stats
{{ end }}

frontend rotating_proxies
  bind *:{{.Port}}
  default_backend privoxies
  option http_proxy

backend privoxies
  balance roundrobin
  timeout http-keep-alive 3000

  option forwardfor
  option http-server-close
  option http_proxy
  {{ range $port, $be := .Backends }}
  server privoxy-{{ $port }} 127.0.0.1:{{ $port }} check{{ end }}
`

// HAProxy helps manage an instance of HAProxy.
type HAProxy struct {
	log zap.Logger
	cmd *Cmd

	dir      string
	conf     string
	template *template.Template
	mu       sync.Mutex
	delay    *time.Timer
	reloadQ  chan bool

	EnableStats bool
	MaxConn     int
	PidFile     string
	Port        int
	StatsPort   int
	Backends    map[int]struct{}
}

func NewHAProxy(ctx context.Context, port int) (h *HAProxy, err error) {
	h = &HAProxy{
		log:     log.With(zap.String("service", "haproxy"), zap.Int("port", port)),
		dir:     "/tmp/torotator/haproxy",
		delay:   time.NewTimer(2 * time.Second),
		reloadQ: make(chan bool, 1),

		EnableStats: *statsPort > 0,
		MaxConn:     256,
		Port:        port,
		StatsPort:   *statsPort,
		Backends:    make(map[int]struct{}),
	}

	t := template.New("haproxy")
	if h.template, err = t.Parse(HAPROXY_TPL); err != nil {
		h.log.Error("unable to parse template", zap.Error(err))
		return
	}

	h.conf = path.Join(h.dir, "haproxy.cfg")
	h.PidFile = path.Join(h.dir, "haproxy.pid")

	if err = h.WriteConfig(ctx, false); err != nil {
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

// MakeDirs attempts to create the directory where HAProxy-related files will reside.
func (h *HAProxy) MakeDirs() (err error) {
	if err = os.MkdirAll(h.dir, 0755); err != nil {
		return
	}

	return nil
}

// HAProxyLogger processes each message received from HAProxy's stdout and stderr. It attempt to categorize each
// message with the correct logging level based on the content of the log line.
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

// WriteConfig persists the current HAProxy configuration to disk. This may also signal the current instance of HAProxy
// to reload the configuration after it's written to disk.
func (h *HAProxy) WriteConfig(ctx context.Context, reload bool) (err error) {
	var f *os.File

	if err = h.MakeDirs(); err != nil {
		return
	}

	if f, err = os.OpenFile(h.conf, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644); err != nil {
		return
	}
	defer f.Close()

	h.mu.Lock()
	err = h.template.Execute(f, h)
	h.mu.Unlock()

	if err != nil {
		h.log.Error("unable to render template", zap.Error(err))
		return
	}

	if reload {
		if err = h.Reload(ctx); err != nil {
			h.log.Error("failed to gracefully reload", zap.Error(err))
			return
		}
	}

	return nil
}

// Reload instructs the current instance of HAProxy to finish serving requests, after which a new instance will replace
// it using the newest configuration. This function attempts to throttle requests to reload HAProxy, as many
// Tor+Privoxy pairs may expire at roughly the same time.
func (h *HAProxy) Reload(ctx context.Context) (err error) {
	if !h.delay.Stop() {
		select {
		case <-h.delay.C:
			// drain channel, jic
		default:
		}
	}

	// delay reload for 2 more seconds
	h.delay.Reset(2 * time.Second)
	select {
	case h.reloadQ <- true:
		h.log.Debug("reload queued")
	default:
		h.log.Debug("reload already queued")
		return
	}

	// if we get this far, empty queue when we leave this function. This indicates that a reload may be re-queued.
	defer func() {
		<-h.reloadQ
	}()

	// wait for the timer to expire
	select {
	case <-h.delay.C:
		h.delay.Stop()

	case <-time.After(10 * time.Second):
		// safety net in case we get into a weird state
		return
	}

	prev := h.cmd

	// start a new instance of HAProxy that should allow the current instance to finish up nicely before the new
	// instance takes over
	h.cmd, err = NewCommand(ctx, h.log, "haproxy",
		"-f", h.conf,
		"-sf", fmt.Sprintf("%d", h.cmd.Pid()))
	if err != nil {
		h.log.Error("failed to start new instance", zap.Error(err))
		return
	}

	// try to not leave zombies
	if err = prev.Close(); err != nil {
		h.log.Warn("failed to clean up previous instance", zap.Error(err))
	}

	return nil
}

// AddBackend tells HAProxy that a new Tor+Privoxy backend is available for use.
func (h *HAProxy) AddBackend(ctx context.Context, port int) {
	h.mu.Lock()
	h.Backends[port] = struct{}{}
	h.mu.Unlock()

	h.WriteConfig(ctx, true)
}

// RemoveBackend tells HAProxy that a Tor+Privoxy backend has expired and should be removed from the pool.
func (h *HAProxy) RemoveBackend(ctx context.Context, port int) {
	h.mu.Lock()
	delete(h.Backends, port)
	h.mu.Unlock()

	h.WriteConfig(ctx, true)
}

func (h *HAProxy) Done() <-chan struct{} {
	return h.cmd.Done()
}

func (h *HAProxy) Wait() {
	h.cmd.Wait()
}

func (h *HAProxy) Close() (err error) {
	if h == nil || h.cmd == nil {
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
