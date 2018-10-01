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

	pb "google.golang.org/genproto/googleapis/devtools/cloudbuild/v1"
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
		input: "_FOO=1+1=2,_BAR=baz", // equal sign in substitution value
		want:  map[string]string{"_FOO": "1+1=2", "_BAR": "baz"},
	}, {
		input: "_FOO=1+1=2=4-2=5-3,_BAR=baz", // equal sign in substitution value
		want:  map[string]string{"_FOO": "1+1=2=4-2=5-3", "_BAR": "baz"},
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

func TestSubstituteAndValidate(t *testing.T) {
	for _, tc := range []struct {
		desc     string
		build    *pb.Build
		substMap map[string]string
		wantErr  bool
		want     map[string]string
	}{{
		desc: "empty substitutions",
		build: &pb.Build{
			Steps:         []*pb.BuildStep{{Name: "ubuntu"}},
			Substitutions: make(map[string]string),
		},
		want: make(map[string]string),
	}, {
		desc: "substitution set in config file",
		build: &pb.Build{
			Steps:         []*pb.BuildStep{{Name: "${_NAME}"}},
			Substitutions: map[string]string{"_NAME": "foo"},
		},
		want: map[string]string{"_NAME": "foo"},
	}, {
		desc: "substitution set in command line flag",
		build: &pb.Build{
			Steps: []*pb.BuildStep{{Name: "${_NAME}"}},
		},
		substMap: map[string]string{"_NAME": "foo"},
		want:     map[string]string{"_NAME": "foo"},
	}, {
		desc: "one substitution overridden in command line",
		build: &pb.Build{
			Steps: []*pb.BuildStep{{
				Name: "${_NAME}${_CITY}",
				Args: []string{"Arlon", "is", "the", "capital", "of", "the", "world"},
			}},
			Substitutions: map[string]string{"_NAME": "foo", "_CITY": "arlon"},
		},
		substMap: map[string]string{"_NAME": "bar"},
		want:     map[string]string{"_NAME": "bar", "_CITY": "arlon"},
	}, {
		desc: "do not override built-in substitution in build config",
		build: &pb.Build{
			Steps:         []*pb.BuildStep{{Name: "ubuntu"}},
			Substitutions: map[string]string{"REPO_NAME": "foo"},
		},
		wantErr: true,
	}, {
		desc: "overridable built-in substitutions",
		build: &pb.Build{
			Steps: []*pb.BuildStep{{Name: "${REPO_NAME}${BRANCH_NAME}${TAG_NAME}${REVISION_ID}${COMMIT_SHA}${SHORT_SHA}"}},
		},
		substMap: map[string]string{"REPO_NAME": "bar", "BRANCH_NAME": "bar", "TAG_NAME": "bar", "REVISION_ID": "bar", "COMMIT_SHA": "bar", "SHORT_SHA": "bar"},
		want:     map[string]string{"REPO_NAME": "bar", "BRANCH_NAME": "bar", "TAG_NAME": "bar", "REVISION_ID": "bar", "COMMIT_SHA": "bar", "SHORT_SHA": "bar"},
	}, {
		desc: "not overridable built-in substitutions",
		build: &pb.Build{
			Steps: []*pb.BuildStep{{Name: "${PROJECT_ID}${BUILD_ID}"}},
		},
		substMap: map[string]string{"PROJECT_ID": "foo", "BUILD_ID": "bar"},
		wantErr:  true,
	}} {
		err := SubstituteAndValidate(tc.build, tc.substMap)
		if err != nil && !tc.wantErr {
			t.Errorf("Got an unexpected error: %v", err)
		}
		if err == nil && tc.wantErr {
			t.Error("Should have returned an error")
		}
		if got := tc.build.Substitutions; err == nil && !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %q; want %q", tc.desc, got, tc.want)
		}
	}
}
