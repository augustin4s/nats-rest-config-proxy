// Copyright 2018 The NATS Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

const (
	Version = "0.0.1"
	AppName = "nats-acl-proxy"
)

// Server is the server.
type Server struct {
	mu sync.Mutex

	// opts is the set of options.
	opts *Options

	// quit stops the server.
	quit func()

	// log is the Logger from the server.
	log Logger
}

// NewServer returns a configured server.
func NewServer(opts *Options) *Server {
	if opts == nil {
		opts = &Options{}
	}
	s := &Server{
		opts: opts,
	}
	s.configureLogger(opts)

	return s
}

func (s *Server) configureLogger(opts *Options) {
	logger := NewDefaultLogger()
	logger.debug = opts.Debug
	logger.trace = opts.Trace
	s.log = logger
}

// Run starts the server.
func (s *Server) Run(ctx context.Context) error {
	s.log.Infof("Starting %s v%s\n", AppName, Version)
	if !s.opts.NoSignals {
		go s.SetupSignalHandler(ctx)
	}

	// Set up cancellation context for the main loop.
	ctx, cancelFn := context.WithCancel(ctx)

	s.quit = func() {
		// Signal cancellation of the main context.
		cancelFn()
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Shutdown stops the controller.
func (s *Server) Shutdown() {
	s.quit()
	s.log.Infof("Bye...")
	return
}

// SetupSignalHandler enables handling process signals.
func (s *Server) SetupSignalHandler(ctx context.Context) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	for sig := range sigCh {
		s.log.Debugf("Trapped '%v' signal\n", sig)

		// If main context already done, then just skip
		select {
		case <-ctx.Done():
			continue
		default:
		}

		switch sig {
		case syscall.SIGINT:
			s.log.Debugf("Exiting...")
			os.Exit(0)
			return
		case syscall.SIGTERM:
			// Gracefully shutdown the server.
			s.Shutdown()
			return
		}
	}
}
