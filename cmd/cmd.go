package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os/exec"
	"time"

	"github.com/uber-go/zap"
)

type Cmd struct {
	log    zap.Logger
	cmd    *exec.Cmd
	stdout io.ReadCloser
	stderr io.ReadCloser
	done   chan struct{}

	transformLog func(string) (string, string, []zap.Field)
}

func NewCommand(ctx context.Context, log zap.Logger, name string, args ...string) (c *Cmd, err error) {
	c = &Cmd{
		log:  log,
		cmd:  exec.CommandContext(ctx, name, args...),
		done: make(chan struct{}),
	}

	if c.stdout, err = c.cmd.StdoutPipe(); err != nil {
		c.log.Error("failed to setup stdout pipe", zap.Error(err))
	}

	if c.stderr, err = c.cmd.StderrPipe(); err != nil {
		c.log.Error("failed to setup stderr pipe", zap.Error(err))
	}

	if err = c.cmd.Start(); err != nil {
		c.log.Error("failed to start", zap.Error(err))
		return nil, err
	}

	c.log = c.log.With(zap.Int("pid", c.cmd.Process.Pid))

	time.Sleep(250 * time.Millisecond)
	if c.cmd.ProcessState != nil {
		return nil, errors.New(c.cmd.ProcessState.String())
	}

	c.log.Info("running")

	return c, nil
}

func (c *Cmd) Pid() int {
	if c.cmd == nil {
		return -1
	}

	return c.cmd.Process.Pid
}

func (c *Cmd) Done() <-chan struct{} {
	return c.done
}

func (c *Cmd) Wait() {
	var (
		line   string
		fields []zap.Field
		level  string
		lf     func(string, ...zap.Field)
	)

	r := io.MultiReader(c.stdout, c.stderr)
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		// extract log level information from Tor messages
		line = scanner.Text()
		fields = fields[:]

		if c.transformLog != nil {
			level, line, fields = c.transformLog(line)
		}

		switch level {
		case "debug":
			lf = c.log.Debug
		case "warn":
			lf = c.log.Warn
		case "err", "fatal":
			lf = c.log.Error
		default:
			lf = c.log.Info
		}

		lf(line, fields...)
	}

	if err := scanner.Err(); err != nil {
		c.log.Error("output error", zap.Error(err))
	}

	c.cmd.Wait()
	close(c.done)
}

func (c *Cmd) Close() (err error) {
	// presence of ProcessState means the process has already exited
	if c.cmd.ProcessState != nil {
		return nil
	}

	c.log.Debug("killing process")
	if err = c.cmd.Process.Kill(); err != nil {
		return
	}

	if c.cmd.ProcessState == nil {
		c.log.Debug("waiting for process to exit")
		if err = c.cmd.Wait(); err != nil {
			return
		}
	}

	return nil
}
