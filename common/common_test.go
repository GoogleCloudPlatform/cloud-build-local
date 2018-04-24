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

package common

import (
	"context"
	"fmt"
	"io"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	listIds = `id1
id2
`
)

type mockRunner struct {
	mu           sync.Mutex
	t            *testing.T
	testCaseName string
	commands     []string
	projectID    string
}

func newMockRunner(t *testing.T) *mockRunner {
	return &mockRunner{
		t: t,
	}
}

// startsWith returns true iff arr startsWith parts.
func startsWith(arr []string, parts ...string) bool {
	if len(arr) < len(parts) {
		return false
	}
	for i, p := range parts {
		if arr[i] != p {
			return false
		}
	}
	return true
}

func (r *mockRunner) Run(ctx context.Context, args []string, in io.Reader, out, err io.Writer, _ string) error {
	r.mu.Lock()
	r.commands = append(r.commands, strings.Join(args, " "))
	r.mu.Unlock()

	if startsWith(args, "docker", "ps", "-a", "-q") ||
		startsWith(args, "docker", "network", "ls") ||
		startsWith(args, "docker", "volume", "ls") {
		fmt.Fprintln(out, listIds)
	}

	return nil
}

func (r *mockRunner) MkdirAll(dir string) error {
	return nil
}

func (r *mockRunner) WriteFile(path, contents string) error {
	return nil
}

func (r *mockRunner) Clean() error {
	return nil
}

func TestBackoff(t *testing.T) {
	var testCases = []struct {
		baseDelay, maxDelay time.Duration
		retries             int
		maxResult           time.Duration
	}{
		{0, 0, 0, 0},

		{0, time.Second, 0, 0},
		{0, time.Second, 1, 0},
		{0, time.Second, 2, 0},

		{time.Second, time.Second, 0, time.Second},
		{time.Second, time.Second, 1, time.Second},
		{time.Second, time.Second, 2, time.Second},

		{time.Second, time.Minute, 0, time.Second},
		{time.Second, time.Minute, 1, time.Duration(1e9 * math.Pow(backoffFactor, 1))},
		{time.Second, time.Minute, 2, time.Duration(1e9 * math.Pow(backoffFactor, 2))},
		{time.Second, time.Minute, 3, time.Duration(1e9 * math.Pow(backoffFactor, 3))},
		{time.Second, time.Minute, 4, time.Duration(1e9 * math.Pow(backoffFactor, 4))},
	}

	for _, test := range testCases {
		backoff := Backoff(test.baseDelay, test.maxDelay, test.retries)
		if backoff < 0 || backoff > test.maxResult {
			t.Errorf("backoff(%v, %v, %v) = %v outside [0, %v]",
				test.baseDelay, test.maxDelay, test.retries,
				backoff, test.maxResult)
		}
	}
}

func TestParseSubstitutionsFlag(t *testing.T) {
	var testCases = []struct {
		input   string
		wantErr bool
		want    map[string]string
	}{{
		input: "_FOO=bar",
		want:  map[string]string{"_FOO": "bar"},
	}, {
		input: "_FOO=",
		want:  map[string]string{"_FOO": ""},
	}, {
		input: "_FOO=bar,_BAR=baz",
		want:  map[string]string{"_FOO": "bar", "_BAR": "baz"},
	}, {
		input: "_FOO=bar, _BAR=baz", // space between the pair
		want:  map[string]string{"_FOO": "bar", "_BAR": "baz"},
	}, {
		input:   "_FOO",
		wantErr: true,
	}}

	for _, test := range testCases {
		got, err := ParseSubstitutionsFlag(test.input)
		if err != nil && !test.wantErr {
			t.Errorf("ParseSubstitutionsFlag returned an unexpected error: %v", err)
		}
		if err == nil && test.wantErr {
			t.Error("ParseSubstitutionsFlag should have returned an error")
		}
		if test.want != nil && !reflect.DeepEqual(got, test.want) {
			t.Errorf("ParseSubstitutionsFlag failed; got %+v, want %+v", got, test.want)
		}
	}
}

func TestClean(t *testing.T) {
	r := newMockRunner(t)
	if err := Clean(context.Background(), r); err != nil {
		t.Errorf("Clean failed: %v", err)
	}
	got := strings.Join(r.commands, "\n")
	want := `docker ps -a -q --filter name=step_[0-9]+|cloudbuild_|metadata
docker rm -f id1 id2
docker network ls -q --filter name=cloudbuild
docker network rm id1 id2
docker volume ls -q --filter name=homevol|cloudbuild_
docker volume rm id1 id2`
	if got != want {
		t.Errorf("Commands didn't match!\n===Want:\n%s\n===Got:\n%s", want, got)
	}
}

func TestRefreshDuration(t *testing.T) {
	start := time.Now()

	// control time.Now for tests.
	now = func() time.Time {
		return start
	}

	for _, tc := range []struct {
		desc       string
		expiration time.Time
		want       time.Duration
	}{{
		desc:       "long case",
		expiration: start.Add(time.Hour),
		want:       45*time.Minute + time.Second,
	}, {
		desc:       "short case",
		expiration: start.Add(4 * time.Minute),
		want:       3*time.Minute + time.Second,
	}, {
		desc:       "pathologically short",
		expiration: start.Add(time.Second),
		want:       time.Second,
	}} {
		got := RefreshDuration(tc.expiration)
		if got != tc.want {
			t.Errorf("%s: got %q; want %q", tc.desc, got, tc.want)
		}
	}
}
