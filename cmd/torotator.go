package main

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/uber-go/zap"
)

const (
	VERSION          = "0.1.0"
	TOR_COUNT        = 3
	PORT_RANGE_START = 30000
	HAPROXY_CFG      = "/etc/haproxy.cfg"
	MAX_PROXY_TIME   = 900
	TEST_URL         = "http://echoip.com"
)

var log zap.Logger

func main() {
	ports = make(map[uint]uint)

	log = zap.New(zap.NewJSONEncoder(zap.RFC3339Formatter("time")))
	log.Info("rotating tor proxy", zap.String("version", VERSION))

	ctx := SignalContext()
	wg := new(sync.WaitGroup)

	ha, err := NewHAProxy(ctx, 8080)
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

// Rotate manages pairs of Tor+Privoxy services. Only a specific number of pairs are permitted at one time. When a pair
// expires, a new pair will automatically take its place.
func Rotate(ctx context.Context, wg *sync.WaitGroup, ha *HAProxy) {
	// Used to limit the number of running proxies. This is separate from wg because wg is unbounded.
	c := make(chan bool, TOR_COUNT)

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

// RunProxy creates a Tor node, followed by a Privoxy instance that handles proxying HTTP requests to the new Tor node.
// The HAProxy instance is notified of the new pair so it can reconfigure itself to use the new pair. If either the Tor
// node or the Privoxy service fail, the pair is invalidated and removed from HAProxy.
func RunProxy(ctx context.Context, ha *HAProxy) {
	// create a new tor/privoxy pair
	tor, err := NewTor(ctx)
	if err != nil {
		tor.Close()
		return
	}

	privoxy, err := NewPrivoxy(ctx, tor)
	if err != nil {
		tor.Close()
		privoxy.Close()
		return
	}

	// mark the ports as used
	mapPorts(tor.port, privoxy.port)

	_log := log.With(zap.Uint("tor", tor.port), zap.Uint("privoxy", privoxy.port))
	_log.Info("proxy started")

	// notify HAProxy of the new backend
	ha.AddBackend(ctx, privoxy.port)

	// let the processes run until they terminate
	go tor.Wait()
	go privoxy.Wait()

	// TODO periodically check that this proxy is still functional
	// wait for any of the following events to occur
	select {
	case <-ctx.Done():
		// application terminating
	case <-tor.Done():
		// tor ended
	case <-privoxy.Done():
		// privoxy ended
	case <-time.After(time.Duration(MAX_PROXY_TIME) * time.Second):
		// proxy lifetime expired
	}

	// tell HAProxy to remove this backend
	ha.RemoveBackend(ctx, privoxy.port)

	// clean up after ourselves
	_log.Info("stopping proxy")
	privoxy.Close()
	tor.Close()

	// release the port for later use
	unmapPorts(tor.port, privoxy.port)
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
