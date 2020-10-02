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
	"net/http"
	"os"
	"strings"
	"time"

	pb "google.golang.org/genproto/googleapis/devtools/cloudbuild/v1"
	"github.com/GoogleCloudPlatform/cloud-build-local/runner"
	"github.com/GoogleCloudPlatform/cloud-build-local/subst"
	"github.com/GoogleCloudPlatform/cloud-build-local/validate"
	"golang.org/x/oauth2"
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
		// Limit the number of elements the string can split into to 2; this allows the substitution value string to contain the `=` character.
		keyValue := strings.SplitN(s, "=", 2)
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
	calledAt := now()
	if expiration.Before(calledAt) {
		return time.Duration(0)
	}
	d := expiration.Sub(calledAt)
	if d > 4*time.Second {
		d = time.Duration(float64(d) * .75)
	} else {
		d -= time.Second
		if d < time.Duration(0) {
			d = time.Duration(0) // Force immediate refresh.
		}
	}

	return d
}

// SubstituteAndValidate merges the substitutions from the build
// config and the local flag, validates them and the build.
func SubstituteAndValidate(b *pb.Build, substMap map[string]string) error {
	if b.Substitutions == nil {
		b.Substitutions = make(map[string]string)
	}

	// Do not accept built-in substitutions in the build config.
	if err := validate.CheckSubstitutions(b.Substitutions); err != nil {
		return fmt.Errorf("Error validating build's substitutions: %v", err)
	}

	if err := validate.CheckSubstitutionsLoose(substMap); err != nil {
		return err
	}
	// Add any flag substitutions to the existing substitution map.
	// If the substitution is already defined in the template, the
	// substitutions flag will override the value.
	for k, v := range substMap {
		b.Substitutions[k] = v
	}

	// Validate the build.
	if err := validate.CheckBuild(b); err != nil {
		return fmt.Errorf("Error validating build: %v", err)
	}

	// Apply substitutions.
	if err := subst.SubstituteBuildFields(b); err != nil {
		return fmt.Errorf("Error applying substitutions: %v", err)
	}

	// Validate the build after substitutions.
	if err := validate.CheckBuildAfterSubstitutions(b); err != nil {
		return fmt.Errorf("Error validating build after substitutions: %v", err)
	}

	return nil
}

// TokenTransport is a RoundTripper that automatically applies OAuth
// credentials from the token source.
type TokenTransport struct {
	Ts oauth2.TokenSource
}

// RoundTrip executes a single HTTP transaction, obtaining the Response for a given Request.
func (t *TokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tok, err := t.Ts.Token()
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	return http.DefaultTransport.RoundTrip(req)
}
