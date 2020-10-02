// Copyright 2017 Google, Inc. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package runner implements method to run commands.
package runner

import (
	"context"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"sync"
)

// Runner is a mockable interface for running os commands.
type Runner interface {
	Run(ctx context.Context, args []string, in io.Reader, out, err io.Writer, dir string) error
	MkdirAll(dir string) error
	WriteFile(path, contents string) error
	Clean() error
}

// RealRunner runs actual os commands.  Tests can define a mocked alternative.
type RealRunner struct {
	DryRun    bool
	mu        sync.Mutex
	processes map[int]*os.Process
}

// Run runs a command.
func (r *RealRunner) Run(ctx context.Context, args []string, in io.Reader, out, err io.Writer, dir string) error {
	if r.DryRun {
		log.Printf("RUNNER - %v", args)
		return nil
	}

	cmd := exec.Command(args[0], args[1:]...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Stdin = in
	cmd.Stdout = out
	cmd.Stderr = err

	if err := cmd.Start(); err != nil {
		return err
	}

	// If the process is started, store it until it completes (defer).
	r.mu.Lock()
	if r.processes == nil {
		r.processes = map[int]*os.Process{}
	}
	r.processes[cmd.Process.Pid] = cmd.Process
	r.mu.Unlock()
	defer func(pid int) {
		r.mu.Lock()
		delete(r.processes, pid)
		r.mu.Unlock()
	}(cmd.Process.Pid)

	errCh := make(chan error)
	go func() {
		errCh <- cmd.Wait()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		// Make an effort to kill the running process, but don't block on it.
		go func() {
			if err := cmd.Process.Kill(); err != nil {
				log.Printf("RUNNER failed to kill running process `%v %v`: %v", cmd.Path, cmd.Args, err)
			}
		}()
		return ctx.Err()
	}
}

// MkdirAll calls MkdirAll.
func (r *RealRunner) MkdirAll(dir string) error {
	return os.MkdirAll(dir, 0755)
}

// WriteFile writes a text file to disk.
func (r *RealRunner) WriteFile(path, contents string) error {
	// Note: os.Create uses mode=0666.
	return ioutil.WriteFile(path, []byte(contents), 0666)
}

// Clean kills running processes.
func (r *RealRunner) Clean() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for pid, p := range r.processes {
		if err := p.Kill(); err != nil {
			return err
		}
		delete(r.processes, pid)
	}
	return nil
}
