package main

import (
	"context"
	"flag"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	VERSION = "dev"

	proxyPort      = flag.Int("p", 8080, "HTTP proxy port")
	torCount       = flag.Int("c", 3, "number of Tor nodes to use")
	portRangeStart = flag.Int("s", 30000, "starting port for proxy usage")
	maxProxyTime   = flag.Int("m", 900, "maximum time (in seconds) a proxy should remain online before being recycled")
	circuitTime    = flag.Int("t", 120, "maximum time (in seconds) a Tor node should be online before recircuiting")
	statsPort      = flag.Int("stats", 0, "serve HAProxy stats on this port")
	debug          = flag.Bool("debug", false, "enable debug mode")
	version        = flag.Bool("v", false, "show version and exit")

	log *zap.Logger
)

func init() {
	var err error

	flag.Parse()

	cfg := zap.NewProductionConfig()
	cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	cfg.EncoderConfig.EncodeDuration = zapcore.StringDurationEncoder

	if *debug {
		cfg.Development = true
	}

	if log, err = cfg.Build(); err != nil {
		panic(err)
	}

	log.Info("rotating tor proxy", zap.String("version", VERSION))
	if *version {
		os.Exit(0)
	}
}

func main() {
	FindDependencies()

	ctx := SignalContext()
	wg := new(sync.WaitGroup)

	ha, err := NewHAProxy(ctx, *proxyPort)
	if err != nil {
		log.Fatal("failed to start HAproxy", zap.Error(err))
	}

	defer ha.Close()
	go ha.Wait()
	go ReloadOnHUP(ctx, ha)

	Rotate(ctx, wg, ha)

	// clean up
	wg.Wait()
	log.Info("done")
}

func FindDependencies() {
	var (
		found string
		err   error
	)

	deps := []string{"haproxy", "tor"}
	for _, dep := range deps {
		if found, err = exec.LookPath(dep); err != nil {
			log.Fatal("missing required program", zap.String("name", dep))
		} else {
			log.Debug("found required program", zap.String("name", dep), zap.String("path", found))
		}
	}
}

// Rotate manages pairs of Tor+Privoxy services. Only a specific number of pairs are permitted at one time. When a pair
// expires, a new pair will automatically take its place.
func Rotate(ctx context.Context, wg *sync.WaitGroup, ha *HAProxy) {
	// Used to limit the number of running proxies. This is separate from wg because wg is unbounded.
	c := make(chan bool, *torCount)

	for {
		select {
		case <-ctx.Done():
			// application terminating
			close(c)
			return

		default:
			c <- true

			// time to create a new pair
			wg.Add(1)
			go func() {
				RunProxy(ctx, ha)

				wg.Done()
				<-c
			}()
		}
	}
}

// RunProxy creates a Tor node and notifies HAProxy so it can reconfigure itself to use the new pair. If the Tor node
// fails, the circuit is invalidated and removed from HAProxy.
func RunProxy(ctx context.Context, ha *HAProxy) {
	tor, err := NewTor(ctx)
	if err != nil {
		tor.Close()
		return
	}

	_log := log.With(zap.Int("tor", tor.port))
	_log.Info("proxy started")

	// notify HAProxy of the new backend
	ha.AddBackend(ctx, tor.port)

	// let the processes run until they terminate
	go tor.Wait()

	// TODO periodically check that this proxy is still functional
	// wait for any of the following events to occur
	select {
	case <-ctx.Done():
		// application terminating
	case <-tor.Done():
		// tor ended
	case <-time.After(time.Duration(*maxProxyTime) * time.Second):
		// proxy lifetime expired
	}

	// tell HAProxy to remove this backend
	ha.RemoveBackend(ctx, tor.port)

	// clean up after ourselves
	_log.Info("stopping proxy")
	tor.Close()

	_log.Info("proxy terminated")
}

// SignalContext creates a new context that will be canceled when the program receives certain termination signals.
func SignalContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())

	// handle termination signals
	terminate := make(chan os.Signal, 1)
	signal.Notify(terminate, os.Kill, os.Interrupt)

	go func() {
		<-terminate
		cancel()
	}()

	return ctx
}

// ReloadOnHUP waits to receive a SIGHUP signal, at which point HAProxy will reload its configuration.
func ReloadOnHUP(ctx context.Context, ha *HAProxy) {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)

	go func() {
		for _ = range hup {
			log.Info("got sighup; reloading config")
			ha.Reload(ctx)
		}
	}()
}
