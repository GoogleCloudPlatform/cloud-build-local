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
	"fmt"
	"math/rand"
	"strings"
	"time"
)

const (
	backoffFactor = 1.3 // backoff increases by this factor on each retry
	backoffRange  = 0.4 // backoff is randomized downwards by this factor
)

// LogEntry is a single log entry containing 1 line of text.
type LogEntry struct {
	// Line number for this line of output.
	Line int
	// Label describes the writer generating the log entry. eg, "MAIN" or "3:gcr.io/cloud-builders/some-builder:STDOUT"
	Label string
	// Text is one line of output.
	Text string
	// Time is the time the log line was written.
	Time time.Time
}

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
