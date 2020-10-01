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
	"log"
	"os/exec"
)

// Runner is a mockable interface for running os commands.
type Runner interface {
	Run(ctx context.Context, args []string, in io.Reader, out, err io.Writer) error
}

// RealRunner runs actual os commands.  Tests can define a mocked alternative.
type RealRunner struct {
	DryRun    bool
}

// Run runs a command.
func (r *RealRunner) Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if r.DryRun {
		log.Printf("RUNNER - %v", args)
		return nil
	}

	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr

  err := cmd.Run()

	// If the context is canceled or times out, return error from the context
	// instead of the command. The command error will be `signal: killed` which
	// isn't very useful.
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return err
	}
}
