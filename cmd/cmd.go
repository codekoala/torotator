package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os/exec"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Cmd is a wrapper around exec.Cmd. It allows for stdout and stderr to automatically be logged along with everything
// else in the application. It also provides helpers to check if the process has finished and also to clean up the
// process.
type Cmd struct {
	log    *zap.Logger
	cmd    *exec.Cmd
	stdout io.ReadCloser
	stderr io.ReadCloser
	done   chan struct{}

	transformLog func(string) (string, string, []zapcore.Field)
}

// NewCommand creates a new Cmd that is setup for common logging and state tracking.
func NewCommand(ctx context.Context, log *zap.Logger, name string, args ...string) (c *Cmd, err error) {
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

	// give the process a bit of time to settle
	time.Sleep(250 * time.Millisecond)

	// only ended processes have a non-nil ProcessState
	if c.cmd.ProcessState != nil {
		return nil, errors.New(c.cmd.ProcessState.String())
	}

	c.log.Info("running")

	return c, nil
}

// Pid returns the PID of the underlying command.
func (c *Cmd) Pid() int {
	if c.cmd == nil {
		return -1
	}

	return c.cmd.Process.Pid
}

// Done returns a channel that signals when the process has ended.
func (c *Cmd) Done() <-chan struct{} {
	return c.done
}

// Wait processes output from the process and signals when the process has neded.
func (c *Cmd) Wait() {
	var (
		line   string
		fields []zapcore.Field
		level  string
		lf     func(string, ...zapcore.Field)
	)

	// receive data from both stdout and stderr
	r := io.MultiReader(c.stdout, c.stderr)

	// wait for output
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		// extract log level information from Tor messages
		line = scanner.Text()
		fields = fields[:]

		// optionally process output from the command to make common logging more useful
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

	// wait for the underlying process to finish
	c.cmd.Wait()

	// signal that the command has ended
	close(c.done)
}

// Close does its best to clean up the process.
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
