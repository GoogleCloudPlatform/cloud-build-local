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

// Package common shares methods for local builder.
package common

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"math/rand"
	"os"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/container-builder-local/runner"
)

const (
	backoffFactor = 1.3 // backoff increases by this factor on each retry
	backoffRange  = 0.4 // backoff is randomized downwards by this factor
)

// Backoff returns a value in [0, maxDelay] that increases exponentially with
// retries, starting from baseDelay.
func Backoff(baseDelay, maxDelay time.Duration, retries int) time.Duration {
	backoff, max := float64(baseDelay), float64(maxDelay)
	for backoff < max && retries > 0 {
		backoff = backoff * backoffFactor
		retries--
	}
	if backoff > max {
		backoff = max
	}

	// Randomize backoff delays so that if a cluster of requests start at
	// the same time, they won't operate in lockstep.  We just subtract up
	// to 40% so that we obey maxDelay.
	backoff -= backoff * backoffRange * rand.Float64()
	if backoff < 0 {
		return 0
	}
	return time.Duration(backoff)
}

// ParseSubstitutionsFlag parses a substitutions string into a map.
func ParseSubstitutionsFlag(substitutions string) (map[string]string, error) {
	substitutionsMap := make(map[string]string)
	list := strings.Split(substitutions, ",")
	for _, s := range list {
		keyValue := strings.Split(s, "=")
		if len(keyValue) != 2 {
			return substitutionsMap, fmt.Errorf("The substitution key value pair is not valid: %s", s)
		}
		substitutionsMap[strings.TrimSpace(keyValue[0])] = strings.TrimSpace(keyValue[1])
	}
	return substitutionsMap, nil
}

// Clean removes left-over containers, networks, and volumes from a previous
// run of the local builder. This happens when ctrl+c is used during a local
// build.
// Each cleaning is defined by a get command, a warning to print if the get
// command returns something, and a delete command to apply in that case.
func Clean(ctx context.Context, r runner.Runner) error {
	items := []struct {
		getCmd, deleteCmd []string
		warning           string
	}{{
		getCmd:    []string{"docker", "ps", "-a", "-q", "--filter", "name=step_[0-9]+|cloudbuild_|metadata"},
		deleteCmd: []string{"docker", "rm", "-f"},
		warning:   "Warning: there are left over step containers from a previous build, cleaning them.",
	}, {
		getCmd:    []string{"docker", "network", "ls", "-q", "--filter", "name=cloudbuild"},
		deleteCmd: []string{"docker", "network", "rm"},
		warning:   "Warning: a network is left over from a previous build, cleaning it.",
	}, {
		getCmd:    []string{"docker", "volume", "ls", "-q", "--filter", "name=homevol|cloudbuild_"},
		deleteCmd: []string{"docker", "volume", "rm"},
		warning:   "Warning: there are left over step volumes from a previous build, cleaning it.",
	}}

	for _, item := range items {
		var output bytes.Buffer
		if err := r.Run(ctx, item.getCmd, nil, &output, os.Stderr, ""); err != nil {
			return err
		}

		str := strings.TrimSpace(output.String())
		if str == "" {
			continue
		}
		log.Println(item.warning)

		args := strings.Split(str, "\n")
		deleteCmd := append(item.deleteCmd, args...)
		if err := r.Run(ctx, deleteCmd, nil, nil, os.Stderr, ""); err != nil {
			return err
		}
	}

	return nil
}

// For unit tests.
var now = time.Now

// RefreshDuration calculates when to refresh the access token. We refresh a
// bit prior to the token's expiration.
func RefreshDuration(expiration time.Time) time.Duration {
	d := expiration.Sub(now())
	if d > 4*time.Second {
		d = time.Duration(float64(d)*.75) + time.Second
	}

	return d
}
