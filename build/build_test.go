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

package build

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	durpb "github.com/golang/protobuf/ptypes/duration"
	"github.com/GoogleCloudPlatform/container-builder-local/gsutil"
	"github.com/GoogleCloudPlatform/container-builder-local/runner"
	"github.com/spf13/afero"
	"golang.org/x/oauth2"
	"github.com/google/uuid"

	pb "google.golang.org/genproto/googleapis/devtools/cloudbuild/v1"
)

const (
	uuidRegex = "([a-fA-F0-9]{8}-[a-fA-F0-9]{4}-4[a-fA-F0-9]{3}-[8|9|aA|bB][a-fA-F0-9]{3}-[a-fA-F0-9]{12})"
)

type mockRunner struct {
	mu               sync.Mutex
	t                *testing.T
	testCaseName     string
	commands         []string
	localImages      map[string]bool
	remoteImages     map[string]bool
	remotePushesFail bool
	remotePullsFail  bool
	dockerRunHandler func(args []string, out, err io.Writer) error
	localFiles       map[string]string
	volumes          map[string]bool
}

func newMockRunner(t *testing.T, testCaseName string) *mockRunner {
	return &mockRunner{
		t:            t,
		testCaseName: testCaseName,
		localImages: map[string]bool{
			"gcr.io/cached-build-step": true,
			"gcr.io/step-zero":         true,
			"gcr.io/step-one":          true,
			"gcr.io/step-two":          true,
			"gcr.io/step-three":        true,
			"gcr.io/step-four":         true,
			"gcr.io/step-five":         true,
			"gcr.io/step-six":          true,
			"gcr.io/step-seven":        true,
			"gcr.io/step-eight":        true,
			"gcr.io/step-nine":         true,
			"gcr.io/step-ten":          true,
		},
		remoteImages: map[string]bool{
			"gcr.io/my-project/my-compiler": true,
			"gcr.io/my-project/my-builder":  true,
		},
		localFiles: map[string]string{},
		volumes:    map[string]bool{},
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

// contains returns true iff arr contains parts in order, considering only the
// first occurrence of the first part.
func contains(arr []string, parts ...string) bool {
	if len(arr) < len(parts) {
		return false
	}
	for i, a := range arr {
		if a == parts[0] {
			return startsWith(arr[i:], parts...)
		}
	}
	return false
}

func (r *mockRunner) MkdirAll(dir string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commands = append(r.commands, fmt.Sprintf("MkdirAll(%s)", dir))
	return nil
}

func (r *mockRunner) WriteFile(path, contents string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commands = append(r.commands, fmt.Sprintf("WriteFile(%s,%q)", path, contents))
	return nil
}

func (r *mockRunner) Clean() error {
	return nil
}

func (r *mockRunner) gsutil(ctx context.Context, args []string, in io.Reader, out, err io.Writer) error {
	if startsWith(args, "cp") {
		uri := args[1]
		filename := args[2]

		switch uri {
		case "gs://some-bucket/source.zip":
			r.localFiles[filename] = "FAKE_ZIPFILE_CONTENTS"
			return nil
		case "gs://some-bucket/source.zip#1234":
			r.localFiles[filename] = "FAKE_ZIPFILE_CONTENTS"
			return nil
		case "gs://some-bucket/fail-unpack.zip":
			r.localFiles[filename] = "FAKE_ZIPFILE_THAT_FAILS_TO_UNPACK"
			return nil
		case "gs://some-bucket/source.tgz":
			r.localFiles[filename] = "FAKE_TARBALL_CONTENTS"
			return nil
		case "gs://some-bucket/source.foo_bar_baz":
			r.localFiles[filename] = "FAKE_TARBALL_CONTENTS"
			return nil
		case "gs://some-bucket/fail-fetch.tgz":
			r.localFiles[filename] = "INCOMPLETE_TARBALL"
			return fmt.Errorf("exit status 1")
		case "gs://some-bucket/fail-unpack.tgz":
			r.localFiles[filename] = "FAKE_TARBALL_THAT_FAILS_TO_UNPACK"
			return nil
		default:
			r.t.Errorf("%s: Unexpected gsutil URI: %q", r.testCaseName, uri)
			return nil
		}
	}
	if startsWith(args, "cat") {
		uri := args[2]
		switch uri {
		case "gs://some-bucket/source.tgz":
			fmt.Fprint(out, "FAKE_TARBALL_CONTENTS")
			return nil
		case "gs://some-bucket/fail-fetch.tgz":
			fmt.Fprint(out, "INCOMPLETE_TARBALL")
			return fmt.Errorf("exit status 1")
		case "gs://some-bucket/fail-unpack.tgz":
			fmt.Fprint(out, "FAKE_TARBALL_THAT_FAILS_TO_UNPACK")
			return nil
		default:
			r.t.Errorf("%s: Unexpected gsutil URI: %q", r.testCaseName, uri)
			return nil
		}
	}

	return fmt.Errorf("%s: unexpected gsutil command passed to mockRunner: %q", r.testCaseName, args)
}

func (r *mockRunner) Run(ctx context.Context, args []string, in io.Reader, out, err io.Writer, _ string) error {
	
	r.mu.Lock()
	r.commands = append(r.commands, strings.Join(args, " "))
	r.mu.Unlock()

	if contains(args, "sleep") {
		// Sleep duration is the last argument.
		sleepArg := args[len(args)-1]
		// Parse the duration argument as milliseconds.
		dur, err := time.ParseDuration(fmt.Sprintf("%s%s", sleepArg, "ms"))
		if err != nil {
			return fmt.Errorf("bad args for 'sleep': got %s, want integer value", args[1])
		}
		time.Sleep(dur)
	}
	if startsWith(args, "gsutil") {
		return r.gsutil(ctx, args[1:], in, out, err)
	}
	if startsWith(args, "docker", "images") {
		if args[2] != "-q" {
			return errors.New("bad args for 'docker image'")
		}
		tag := args[3]
		if r.localImages[tag] {
			io.WriteString(out, "digestLocal")
		}
		// if it's not there, write nothing but still no error.
		return nil
	}
	if startsWith(args, "docker", "inspect") {
		tag := args[2]
		r.mu.Lock()
		localImage := r.localImages[tag]
		r.mu.Unlock()
		if localImage {
			// Image exists.
			io.WriteString(out, `[
{
"Id": "blah",
"RepoDigests": [
"gcr.io/cached-build-step@sha256:digestLocal"
],
"Config": {
"Image": "sha256:notReadDigest"
}
}
]
`)
			return nil
		}
		// Image not present.
		return errors.New("exit status 1")
	}
	if startsWith(args, "docker", "run") && contains(args, "gcr.io/cloud-builders/docker", "pull") {
		if r.remotePullsFail {
			// Failed pull.
			return errors.New("exit status 1")
		}
		tag := args[len(args)-1]
		if r.remoteImages[tag] {
			// Successful pull.
			io.WriteString(out, `Using default tag: latest
latest: Pulling from test/busybox
a5d4c53980c6: Pull complete
b41c5284db84: Pull complete
Digest: sha256:digestRemote
Status: Downloaded newer image for gcr.io/test/busybox:latest
`)
			r.localImages[tag] = true
			return nil
		}
		// Failed pull.
		return fmt.Errorf("exit status 1 for tag %q", tag)
	}
	if startsWith(args, "docker", "run") && contains(args, "gcr.io/cloud-builders/docker", "push") {
		if r.remotePushesFail {
			// Failed push.
			return errors.New("exit status 1")
		}
		tag := args[len(args)-1]
		io.WriteString(out, `The push refers to a repository [skelterjohn/ubuntu] (len: 1)
ca4d7b1b9a51: Image already exists
a467a7c6794f: Image successfully pushed
ea358092da77: Image successfully pushed
2332d8973c93: Image successfully pushed
`)
		if tag == "gcr.io/some-image-1" {
			io.WriteString(out, "latest: digest: sha256:0h10b33f000deadb33f0000123456789abcdeffffffffffffff size: 9914\n")
		} else if tag == "gcr.io/some-image-2" {
			io.WriteString(out, "latest: digest: sha256:0h10n3rd000deadb33f0000123456789abcdeffffffffffffff size: 9914\n")
		} else if tag == "gcr.io/some-image-3" {
			io.WriteString(out, "latest: digest: sha256:0h10l33t000deadb33f0000123456789abcdeffffffffffffff size: 9914\n")
		} else if tag != "gcr.io/build-output-tag-no-digest" {
			io.WriteString(out, "latest: digest: sha256:deadb33f000deadb33f0000123456789abcdeffffffffffffff size: 9914\n")
		}

		// Successful push.
		return nil
	}
	if startsWith(args, "docker", "run") && contains(args, "gcr.io/cloud-builders/gsutil") {
		for i, a := range args {
			if a == "gcr.io/cloud-builders/gsutil" && i < len(args)-2 {
				return r.gsutil(ctx, args[i+1:], in, out, err)
			}
		}
		return fmt.Errorf("no commands for gcr.io/cloud-builders/gsutil: %+v", args)
	}
	if startsWith(args, "docker", "run") && r.dockerRunHandler != nil {
		return r.dockerRunHandler(args, out, err)
	}
	if startsWith(args, "docker", "volume") {
		if startsWith(args, "docker", "volume", "create", "--name") {
			volName := args[len(args)-1]
			r.volumes[volName] = true
			return nil
		}
		if startsWith(args, "docker", "volume", "rm") {
			volName := args[len(args)-1]
			if r.volumes[volName] {
				return fmt.Errorf("volume %q has not been created (or was already deleted)", volName)
			}
			delete(r.volumes, volName)
			return nil
		}
		r.t.Errorf("Unexpected docker volume call: %v", args)
		return nil
	}
	if startsWith(args, "docker", "rm") {
		return nil
	}
	if startsWith(args, "unzip") {
		filename := args[1]
		contents := r.localFiles[filename]
		switch contents {
		case "":
			r.t.Errorf("%s: unzip reading file that doesn't exist: %q", r.testCaseName, filename)
		case "FAKE_ZIPFILE_CONTENTS":
			return nil
		case "FAKE_ZIPFILE_THAT_FAILS_TO_UNPACK":
			return fmt.Errorf("Archive: %s ... (~5 lines of text)", filename)
		default:
			r.t.Errorf("%s: Unexpected zipfile contents: %q", r.testCaseName, contents)
			return nil
		}
	}
	if startsWith(args, "tar") {
		var contents string
		if startsWith(args, "tar", "-xzf") {
			filename := args[2]
			contents = r.localFiles[filename]
		} else {
			// Consume all of stdin.
			i, err := ioutil.ReadAll(in)
			if err != nil {
				// This is all that production would print!
				return errors.New("exit status 2")
			}
			contents = string(i)
		}
		switch contents {
		case "FAKE_TARBALL_CONTENTS":
			return nil
		case "FAKE_TARBALL_THAT_FAILS_TO_UNPACK":
			return fmt.Errorf("gzip: stdin: not in gzip format")
		case "INCOMPLETE_TARBALL":
			return fmt.Errorf("an error that we won't see")
		default:
			r.t.Errorf("%s: Unexpected tarball contents: %q", r.testCaseName, contents)
			return nil
		}
	}
	if startsWith(args, "/root/write_docker_creds.bash") {
		return nil
	}

	r.t.Errorf("%s: unexpected command passed to mockRunner: %q", r.testCaseName, args)
	return nil
}

type mockServer struct {
	t               *testing.T
	failNextRequest bool
}

func (s *mockServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.failNextRequest {
		// this feature is introduced to exercise retry logic, so one failure is good enough.
		s.failNextRequest = false
		http.Error(w, "not found", http.StatusNotFound)
	}
	if r.Method == "GET" && r.URL.String() == "http://source.developers.google.com/p/PROJECT/r/REPO/archive/REVISION.tar.gz" {
		fmt.Fprint(w, "FAKE_TARBALL_CONTENTS")
	} else if r.Method == "GET" && r.URL.String() == "http://source.developers.google.com/url-that-fails-to-fetch" {
		http.Error(w, "an expected failure", http.StatusTeapot)
	} else if r.Method == "GET" && r.URL.String() == "http://source.developers.google.com/url-that-fails-to-unpack" {
		fmt.Fprintf(w, "FAKE_TARBALL_THAT_FAILS_TO_UNPACK")
	} else {
		content, _ := ioutil.ReadAll(r.Body)
		s.t.Errorf("unexpected request: %s %s\n%s", r.Method, r.URL, string(content))
		http.Error(w, "not found", http.StatusNotFound)
	}
}

var commonBuildRequest = pb.Build{
	Source: &pb.Source{
		Source: &pb.Source_RepoSource{
			RepoSource: &pb.RepoSource{},
		},
	},
	SourceProvenance: &pb.SourceProvenance{
		ResolvedRepoSource: &pb.RepoSource{
			Revision: &pb.RepoSource_CommitSha{CommitSha: "commit-sha"},
		},
	},
	Steps: []*pb.BuildStep{{
		Name: "gcr.io/my-project/my-compiler",
	}, {
		Name:    "gcr.io/my-project/my-builder",
		Env:     []string{"FOO=bar", "BAZ=buz"},
		Args:    []string{"a", "b", "c"},
		Dir:     "foo/bar/../baz",
		Volumes: []*pb.Volume{{Name: "myvol", Path: "/foo"}},
	}},
	Images: []string{"gcr.io/build-output-tag-1", "gcr.io/build-output-tag-2", "gcr.io/build-output-tag-no-digest"},
}

func mockTokenSource() oauth2.TokenSource {
	t := &oauth2.Token{
		AccessToken: "FAKE_ACCESS_TOKEN",
		TokenType:   "Bearer",
	}
	return oauth2.StaticTokenSource(t)
}

func TestFetchBuilder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	testCases := []struct {
		name         string
		buildRequest pb.Build
		pullsFail    bool
		wantErr      error
		wantCommands []string
	}{{
		name:         "TestFetchBuilder",
		buildRequest: commonBuildRequest,
		wantCommands: []string{
			"docker inspect gcr.io/my-project/my-compiler",
			"docker run --name cloudbuild_docker_pull_" + uuidRegex + " --rm --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock gcr.io/cloud-builders/docker pull gcr.io/my-project/my-compiler",
			"docker inspect gcr.io/my-project/my-builder",
			"docker run --name cloudbuild_docker_pull_" + uuidRegex + " --rm --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock gcr.io/cloud-builders/docker pull gcr.io/my-project/my-builder",
		},
	}, {
		name: "TestFetchBuilderExists",
		buildRequest: pb.Build{
			Steps: []*pb.BuildStep{{
				Name: "gcr.io/cached-build-step",
			}},
		},
		wantCommands: []string{
			"docker inspect gcr.io/cached-build-step",
		},
	}, {
		name: "TestFetchBuilderFail",
		buildRequest: pb.Build{
			Steps: []*pb.BuildStep{{
				Name: "gcr.io/invalid-build-step",
			}},
		},
		pullsFail: true,
		// no image in remoteImages
		wantCommands: []string{
			"docker inspect gcr.io/invalid-build-step",
			// Retry this 10 times.
			"docker run --name cloudbuild_docker_pull_" + uuidRegex + " --rm --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock gcr.io/cloud-builders/docker pull gcr.io/invalid-build-step",
			"docker run --name cloudbuild_docker_pull_" + uuidRegex + " --rm --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock gcr.io/cloud-builders/docker pull gcr.io/invalid-build-step",
			"docker run --name cloudbuild_docker_pull_" + uuidRegex + " --rm --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock gcr.io/cloud-builders/docker pull gcr.io/invalid-build-step",
			"docker run --name cloudbuild_docker_pull_" + uuidRegex + " --rm --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock gcr.io/cloud-builders/docker pull gcr.io/invalid-build-step",
			"docker run --name cloudbuild_docker_pull_" + uuidRegex + " --rm --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock gcr.io/cloud-builders/docker pull gcr.io/invalid-build-step",
			"docker run --name cloudbuild_docker_pull_" + uuidRegex + " --rm --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock gcr.io/cloud-builders/docker pull gcr.io/invalid-build-step",
			"docker run --name cloudbuild_docker_pull_" + uuidRegex + " --rm --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock gcr.io/cloud-builders/docker pull gcr.io/invalid-build-step",
			"docker run --name cloudbuild_docker_pull_" + uuidRegex + " --rm --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock gcr.io/cloud-builders/docker pull gcr.io/invalid-build-step",
			"docker run --name cloudbuild_docker_pull_" + uuidRegex + " --rm --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock gcr.io/cloud-builders/docker pull gcr.io/invalid-build-step",
			"docker run --name cloudbuild_docker_pull_" + uuidRegex + " --rm --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock gcr.io/cloud-builders/docker pull gcr.io/invalid-build-step",
			"docker run --name cloudbuild_docker_pull_" + uuidRegex + " --rm --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock gcr.io/cloud-builders/docker pull gcr.io/invalid-build-step",
		},
		wantErr: errors.New(`error pulling build step 0 "gcr.io/invalid-build-step": exit status 1`),
	}}
	for _, tc := range testCases {
		r := newMockRunner(t, tc.name)
		r.remotePullsFail = tc.pullsFail
		b := New(r, tc.buildRequest, mockTokenSource(), nopBuildLogger{}, nopEventLogger{}, "", afero.NewMemMapFs(), true, false, false)
		var gotErr error
		var gotDigest string
		wantDigest := ""
		for i, bs := range tc.buildRequest.Steps {
			gotDigest, gotErr = b.fetchBuilder(ctx, bs.Name, fmt.Sprintf("Step #%d", i), 0)
			if gotErr != nil {
				break
			}
			if tc.name == "TestFetchBuilderExists" {
				wantDigest = "sha256:digestLocal"
			}
			if gotDigest != wantDigest {
				t.Errorf("%s: Digest mismatch, wanted %s, got %v. Request: %v", tc.name, wantDigest, gotDigest, tc.buildRequest)
			}
		}
		if !reflect.DeepEqual(gotErr, tc.wantErr) {
			t.Errorf("%s: Wanted %q, but got %q", tc.name, tc.wantErr, gotErr)
		}
		if tc.wantCommands != nil {
			if len(r.commands) != len(tc.wantCommands) {
				t.Errorf("%s: Wrong number of commands: want %d, got %d", tc.name, len(tc.wantCommands), len(r.commands))
			}
			for i := range r.commands {
				if i >= len(tc.wantCommands) {
					break
				}
				if match, _ := regexp.MatchString(tc.wantCommands[i], r.commands[i]); !match {
					t.Errorf("%s: command %d didn't match!\n===Want:\n%s\n===Got:\n%s", tc.name, i, tc.wantCommands[i], r.commands[i])
				}
			}
			got := strings.Join(r.commands, "\n")
			want := strings.Join(tc.wantCommands, "\n")
			if match, _ := regexp.MatchString(want, got); !match {
				t.Errorf("%s: Commands didn't match!\n===Want:\n%s\n===Got:\n%s", tc.name, want, got)
			}
		}
	}
}

func TestGetWaitChansForStep(t *testing.T) {
	t.Parallel()
	ch1 := make(chan struct{})
	ch2 := make(chan struct{})
	ch3 := make(chan struct{})
	// This slice is used to dynamically find a channel by index for the test cases.
	chans := []chan struct{}{ch1, ch2, ch3}
	testCases := []struct {
		name    string
		build   Build
		index   int
		want    []chan struct{}
		wantErr error
	}{{
		name: "NoDependencies",
		build: Build{
			Request: pb.Build{
				Steps: []*pb.BuildStep{{
					Name: "gcr.io/invalid-build-step0",
				}, {
					Name: "gcr.io/invalid-build-step1",
				}},
			},
		},
		index: 1,
		want: []chan struct{}{
			ch1,
		},
	}, {
		name: "IDWithOneDepenedency",
		build: Build{
			Request: pb.Build{
				Steps: []*pb.BuildStep{{
					Id:      "A",
					WaitFor: []string{StartStep},
				}, {
					Id:      "B",
					WaitFor: []string{"A"},
				}},
			},
		},
		index: 1,

		want: []chan struct{}{
			ch1,
		},
	}, {
		name: "IDWithMultipleDependencies",
		build: Build{
			Request: pb.Build{
				Steps: []*pb.BuildStep{{
					Id:      "A",
					WaitFor: []string{StartStep},
				}, {
					Id:      "B",
					WaitFor: []string{"A"},
				}, {
					Id:      "C",
					WaitFor: []string{"A", "B"},
				}},
			},
		},
		index: 2,
		want: []chan struct{}{
			ch1,
			ch2,
		},
	}, {
		name: "StartStepNoDependencies",
		build: Build{
			Request: pb.Build{
				Steps: []*pb.BuildStep{{
					Name:    "gcr.io/cached-build-step",
					WaitFor: []string{StartStep},
				}},
			},
		},
	}, {
		name: "StartStepsMultipleDependencies",
		build: Build{
			Request: pb.Build{
				Steps: []*pb.BuildStep{{
					Name:    "gcr.io/cached-build-step",
					Id:      "A",
					WaitFor: []string{StartStep},
				}, {
					Name:    "gcr.io/cached-build-step",
					WaitFor: []string{StartStep, "A"},
				}},
			},
		},
		index: 1,
		want: []chan struct{}{
			ch1,
		},
	}, {
		name: "NoIDWithDependency",
		build: Build{
			Request: pb.Build{
				Steps: []*pb.BuildStep{{
					Name:    "gcr.io/cached-build-step",
					Id:      "A",
					WaitFor: []string{StartStep},
				}, {
					Name:    "gcr.io/cached-build-step",
					WaitFor: []string{StartStep, "A"},
				}},
			},
		},
		index: 1,
		want: []chan struct{}{
			ch1,
		},
	}, {
		name: "InvalidBuildTranslateNotPopulated",
		build: Build{
			Request: pb.Build{
				Steps: []*pb.BuildStep{{
					Name:    "gcr.io/cached-build-step",
					Id:      "B",
					WaitFor: []string{"A"},
				}},
			},
		},
		wantErr: fmt.Errorf("build step \"A\" translate not populated"),
	}}

	for _, tc := range testCases {
		tc.build.idxChan = map[int]chan struct{}{}
		tc.build.idxTranslate = map[string]int{}
		for idx, steps := range tc.build.Request.Steps {
			tc.build.idxChan[idx] = chans[idx]
			if len(steps.Id) != 0 {
				tc.build.idxTranslate[steps.Id] = idx
			}
		}
		got, err := tc.build.waitChansForStep(tc.index)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: \n===Got:\n%+v, \n===Want:\n%+v\n", tc.name, got, tc.want)
		}

		switch {
		case err == tc.wantErr:
			// success; do nothing
		case err.Error() != tc.wantErr.Error():
			t.Errorf("%s:\n Got error: %+v\n Want Error: %+v\n", tc.name, err, tc.wantErr)
		}
	}
}

func TestRunBuildSteps(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	exit1Err := errors.New("exit status 1")

	testCases := []struct {
		name                 string
		buildRequest         pb.Build
		opFailsToWrite       bool
		opError              error
		argsOfStepAfterError string
		wantErr              error
		wantCommands         []string
		wantStepStatus       []pb.Build_Status
	}{{
		name:         "TestRunBuilder",
		buildRequest: commonBuildRequest,
		wantCommands: []string{
			"docker volume create --name myvol",
			"docker inspect gcr.io/my-project/my-compiler",
			"docker run --name cloudbuild_docker_pull_" + uuidRegex + " --rm --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock gcr.io/cloud-builders/docker pull gcr.io/my-project/my-compiler",
			dockerRunString(0) + " gcr.io/my-project/my-compiler",
			"docker inspect gcr.io/my-project/my-builder",
			"docker run --name cloudbuild_docker_pull_" + uuidRegex + " --rm --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock gcr.io/cloud-builders/docker pull gcr.io/my-project/my-builder",
			dockerRunInStepDir(1, "foo/baz") +
				" --env FOO=bar" +
				" --env BAZ=buz" +
				" --volume myvol:/foo" +
				" gcr.io/my-project/my-builder a b c",
			"docker images -q gcr.io/build-output-tag-1",
			"docker images -q gcr.io/build-output-tag-2",
			"docker images -q gcr.io/build-output-tag-no-digest",
			"docker rm -f step_0 step_1",
			"docker volume rm myvol",
		},
		wantStepStatus: []pb.Build_Status{pb.Build_SUCCESS, pb.Build_SUCCESS},
	}, {
		name: "TestRunBuilderSubdir",
		buildRequest: pb.Build{
			Source: &pb.Source{
				Source: &pb.Source_RepoSource{
					RepoSource: &pb.RepoSource{
						Dir: "subdir",
					},
				},
			},
			SourceProvenance: &pb.SourceProvenance{
				ResolvedRepoSource: &pb.RepoSource{
					Dir:      "subdir",
					Revision: &pb.RepoSource_CommitSha{CommitSha: "commit-sha"},
				},
			},
			Steps: []*pb.BuildStep{{
				Name: "gcr.io/my-project/my-compiler",
			}, {
				Name:    "gcr.io/my-project/my-builder",
				Env:     []string{"FOO=bar", "BAZ=buz"},
				Args:    []string{"a", "b", "c"},
				Dir:     "foo/bar/../baz",
				Volumes: []*pb.Volume{{Name: "myvol", Path: "/foo"}},
			}},
			Images: []string{"gcr.io/build-output-tag-1", "gcr.io/build-output-tag-2", "gcr.io/build-output-tag-no-digest"},
		},
		wantCommands: []string{
			"docker volume create --name myvol",
			"docker inspect gcr.io/my-project/my-compiler",
			"docker run --name cloudbuild_docker_pull_" + uuidRegex + " --rm --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock gcr.io/cloud-builders/docker pull gcr.io/my-project/my-compiler",
			dockerRunInStepDir(0, "subdir") + " gcr.io/my-project/my-compiler",
			"docker inspect gcr.io/my-project/my-builder",
			"docker run --name cloudbuild_docker_pull_" + uuidRegex + " --rm --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock gcr.io/cloud-builders/docker pull gcr.io/my-project/my-builder",
			dockerRunInStepDir(1, "subdir/foo/baz") +
				" --env FOO=bar" +
				" --env BAZ=buz" +
				" --volume myvol:/foo" +
				" gcr.io/my-project/my-builder a b c",
			"docker images -q gcr.io/build-output-tag-1",
			"docker images -q gcr.io/build-output-tag-2",
			"docker images -q gcr.io/build-output-tag-no-digest",
			"docker rm -f step_0 step_1",
			"docker volume rm myvol",
		},
		wantStepStatus: []pb.Build_Status{pb.Build_SUCCESS, pb.Build_SUCCESS},
	}, {
		name:           "TestRunBuilderFailExplicit",
		buildRequest:   commonBuildRequest,
		opError:        exit1Err,
		wantErr:        errors.New(`build step 0 "gcr.io/my-project/my-compiler" failed: exit status 1`),
		wantStepStatus: []pb.Build_Status{pb.Build_FAILURE, pb.Build_QUEUED},
	}, {
		name:           "TestRunBuilderFailImplicit",
		buildRequest:   commonBuildRequest,
		opFailsToWrite: true,
		wantErr:        errors.New(`failed to find one or more images after execution of build steps: ["gcr.io/build-output-tag-1" "gcr.io/build-output-tag-no-digest"]`),
		wantStepStatus: []pb.Build_Status{pb.Build_SUCCESS, pb.Build_SUCCESS},
	}, {
		name:           "TestRunBuilderFailConfirmNoContinuation",
		buildRequest:   commonBuildRequest,
		opError:        exit1Err,
		wantErr:        errors.New(`build step 0 "gcr.io/my-project/my-compiler" failed: exit status 1`),
		wantStepStatus: []pb.Build_Status{pb.Build_FAILURE, pb.Build_QUEUED},
	}, {
		name:           "Step Timeout",
		buildRequest:   commonBuildRequest,
		opError:        context.DeadlineExceeded,
		wantErr:        fmt.Errorf(`build step 0 "gcr.io/my-project/my-compiler" failed: %v`, context.DeadlineExceeded),
		wantStepStatus: []pb.Build_Status{pb.Build_TIMEOUT, pb.Build_QUEUED},
	}, {
		name:           "Step Canceled",
		buildRequest:   commonBuildRequest,
		opError:        context.Canceled,
		wantErr:        fmt.Errorf(`build step 0 "gcr.io/my-project/my-compiler" failed: %v`, context.Canceled),
		wantStepStatus: []pb.Build_Status{pb.Build_CANCELLED, pb.Build_QUEUED},
	}}
	for _, tc := range testCases {
		r := newMockRunner(t, tc.name)
		r.dockerRunHandler = func([]string, io.Writer, io.Writer) error {
			if !tc.opFailsToWrite {
				r.localImages["gcr.io/build-output-tag-1"] = true
				r.localImages["gcr.io/build-output-tag-no-digest"] = true
			}
			r.localImages["gcr.io/build-output-tag-2"] = true
			return tc.opError
		}
		b := New(r, tc.buildRequest, mockTokenSource(), nopBuildLogger{}, nopEventLogger{}, "", afero.NewMemMapFs(), true, false, false)
		gotErr := b.runBuildSteps(ctx)
		if !reflect.DeepEqual(gotErr, tc.wantErr) {
			t.Errorf("%s: Wanted error %q, but got %q", tc.name, tc.wantErr, gotErr)
		}
		if tc.wantCommands != nil {
			got := strings.Join(r.commands, "\n")
			want := strings.Join(tc.wantCommands, "\n")
			if match, _ := regexp.MatchString(want, got); !match {
				t.Errorf("%s: Commands didn't match!\n===Want:\n%s\n===Got:\n%s", tc.name, want, got)
			}
		}

		b.Mu.Lock()
		if len(b.stepStatus) != len(tc.wantStepStatus) {
			t.Errorf("%s: want len(b.stepStatus)==%d, got %d", tc.name, len(tc.wantStepStatus), len(b.stepStatus))
		} else {
			for i, stepStatus := range tc.wantStepStatus {
				if b.stepStatus[i] != stepStatus {
					t.Errorf("%s step %d: want %s, got %s", tc.name, i, stepStatus, b.stepStatus[i])
				}
			}
		}
		b.Mu.Unlock()

		// Confirm proper population of per-step status in BuildSummary.
		summary := b.Summary()
		got := summary.StepStatus
		if len(got) != len(tc.wantStepStatus) {
			t.Errorf("%s: build summary wrong size; want %d, got %d", tc.name, len(tc.wantStepStatus), len(got))
		} else {
			for i, stepStatus := range tc.wantStepStatus {
				if got[i] != stepStatus {
					t.Errorf("%s summary step %d: want %s, got %s", tc.name, i, stepStatus, got[i])
				}
			}
		}
	}
}

func TestBuildStepOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	testCases := []struct {
		name         string
		buildRequest pb.Build
		wantErr      error
		wantCommands []string
	}{{
		name: "TestRunBuilderSequentialOneStep",
		buildRequest: pb.Build{
			Steps: []*pb.BuildStep{{
				Name: "gcr.io/my-project/my-builder",
				Env:  []string{"FOO=bar", "BAZ=buz"},
				Args: []string{"a", "b", "c"},
				Dir:  "foo/bar/../baz",
			}},
		},
		wantCommands: []string{
			dockerRunInStepDir(0, "foo/baz") +
				" --env FOO=bar" +
				" --env BAZ=buz" +
				" gcr.io/my-project/my-builder a b c",
		},
	}, {
		name: "TestRunBuilderSequentialTwoSteps",
		buildRequest: pb.Build{
			Steps: []*pb.BuildStep{{
				Name: "gcr.io/my-project/my-compiler",
			}, {
				Name: "gcr.io/my-project/my-builder",
				Env:  []string{"FOO=bar", "BAZ=buz"},
				Args: []string{"a", "b", "c"},
				Dir:  "foo/bar/../baz",
			}},
		},
		wantCommands: []string{
			dockerRunString(0) + " gcr.io/my-project/my-compiler",
			dockerRunInStepDir(1, "foo/baz") +
				" --env FOO=bar" +
				" --env BAZ=buz" +
				" gcr.io/my-project/my-builder a b c",
		},
	}, {
		name: "TestRunBuilderSequentialTenSteps",
		buildRequest: pb.Build{
			Steps: []*pb.BuildStep{{
				Name: "gcr.io/step-one",
			}, {
				Name: "gcr.io/step-two",
			}, {
				Name: "gcr.io/step-three",
			}, {
				Name: "gcr.io/step-four",
			}, {
				Name: "gcr.io/step-five",
			}, {
				Name: "gcr.io/step-six",
			}, {
				Name: "gcr.io/step-seven",
			}, {
				Name: "gcr.io/step-eight",
			}, {
				Name: "gcr.io/step-nine",
			}, {
				Name: "gcr.io/step-ten",
			}},
		},
		wantCommands: []string{
			dockerRunString(0) + " gcr.io/step-one",
			dockerRunString(1) + " gcr.io/step-two",
			dockerRunString(2) + " gcr.io/step-three",
			dockerRunString(3) + " gcr.io/step-four",
			dockerRunString(4) + " gcr.io/step-five",
			dockerRunString(5) + " gcr.io/step-six",
			dockerRunString(6) + " gcr.io/step-seven",
			dockerRunString(7) + " gcr.io/step-eight",
			dockerRunString(8) + " gcr.io/step-nine",
			dockerRunString(9) + " gcr.io/step-ten",
		},
	}, {
		name: "TestSerialBuildWithIDs",
		buildRequest: pb.Build{
			Steps: []*pb.BuildStep{{
				Name: "gcr.io/step-one",
				Id:   "A",
			}, {
				Name:    "gcr.io/step-two",
				Id:      "B",
				WaitFor: []string{"A"},
			}, {
				Name:    "gcr.io/step-three",
				WaitFor: []string{"B"},
			}},
		},
		wantCommands: []string{
			dockerRunString(0) + " gcr.io/step-one",
			dockerRunString(1) + " gcr.io/step-two",
			dockerRunString(2) + " gcr.io/step-three",
		},
	}, {
		name: "TestParallelBuilds",
		buildRequest: pb.Build{
			Steps: []*pb.BuildStep{{
				Name: "gcr.io/step-one",
				Id:   "I",
				Args: []string{"markCompleted", "I"},
			}, {
				Name: "gcr.io/step-two",
				Id:   "J",
				Args: []string{"J", "checkCompleted", "I"},
			}, {
				Name: "gcr.io/step-three",
				Id:   "D",
				Args: []string{"D", "checkCompleted", "I"},
			}, {
				Name:    "gcr.io/step-four",
				Id:      "F",
				WaitFor: []string{"I"},
				Args:    []string{"F", "checkCompleted", "I"},
			}, {
				Name:    "gcr.io/step-five",
				Id:      "G",
				WaitFor: []string{"I"},
				Args:    []string{"G", "checkCompleted", "I"},
			}, {
				Name:    "gcr.io/step-six",
				Id:      "H",
				WaitFor: []string{"J"},
				Args:    []string{"H", "checkCompleted", "J"},
			}, {
				Name:    "gcr.io/step-seven",
				Id:      "C",
				WaitFor: []string{"G", "F"},
				Args:    []string{"C", "checkCompleted", "GF"},
			}, {
				Name:    "gcr.io/step-eight",
				Id:      "E",
				WaitFor: []string{"H"},
				Args:    []string{"E", "checkCompleted", "H"},
			}, {
				Name:    "gcr.io/step-nine",
				Id:      "A",
				WaitFor: []string{"C", "D"},
				Args:    []string{"A", "checkCompleted", "CD"},
			}, {
				Name:    "gcr.io/step-ten",
				Id:      "B",
				WaitFor: []string{"D", "E"},
				Args:    []string{"B", "checkCompleted", "DE"},
			}},
		},
	}, {
		name: "TestParallelBuildsMultipleWrites",
		buildRequest: pb.Build{
			Steps: []*pb.BuildStep{{
				Name: "gcr.io/step-one",
				Id:   "I",
				Args: []string{"markCompleted", "I"},
			}, {
				Name: "gcr.io/step-one",
				Id:   "J",
				Args: []string{"markCompleted", "J"},
			}, {
				Name: "gcr.io/step-one",
				Id:   "D",
				Args: []string{"markCompleted", "D"},
			}, {
				Name:    "gcr.io/step-one",
				Id:      "A",
				WaitFor: []string{"I", "J", "D"},
				Args:    []string{"A", "checkCompleted", "IJD"},
			}},
		},
	}}
	for _, tc := range testCases {
		stepArgs := make(chan string)
		r := newMockRunner(t, tc.name)
		// completedSteps stores the ID of a completed build step. completedSteps is accessed
		// to check if a build step's dependencies have already ran.
		completedSteps := make(map[string]bool)
		var mutex = &sync.Mutex{}
		// This mock runner is setup to test dependency ordering.
		// Currently the mock runner takes a command line argument(docker run...)
		// and looks for two key words "checkCompleted" and "markCompleted", which are located at the
		// end of the argument. The format for the write command is <command><ID>.
		// The runner looks for the "markCompleted" keyword and adds the ID to the signal map.
		// The format for the read command is <ID><command><Dependencies>. Once the
		// runner finds "checkCompleted", the runner iterates through the dependencies, which
		// is a string, and checks for the existence of each dependency in the map.
		// If the dependency is not in the map, then the test fails. If all the
		//dependencies are in the map then the ID is added to the map.
		r.dockerRunHandler = func(args []string, _, _ io.Writer) error {
			commandArg := args[len(args)-2]
			argsDeps := args[len(args)-1]
			switch {
			case strings.Contains(commandArg, "markCompleted"):
				mutex.Lock()
				completedSteps[argsDeps] = true
				mutex.Unlock()
				stepArgs <- strings.Join(args, " ")

			case strings.Contains(commandArg, "checkCompleted"):
				stepArgs <- strings.Join(args, " ")
				for _, dep := range argsDeps {
					runeToString := fmt.Sprintf("%c", dep)
					mutex.Lock()
					if ok := completedSteps[runeToString]; !ok {
						t.Errorf("Parallel build steps failed, %q must be called before %q", runeToString, args[len(args)-3])
					}
					mutex.Unlock()
				}
				mutex.Lock()
				completedSteps[args[len(args)-3]] = true
				mutex.Unlock()
			default:
				stepArgs <- strings.Join(args, " ")
			}
			return nil
		}
		b := New(r, tc.buildRequest, mockTokenSource(), nopBuildLogger{}, nopEventLogger{}, "", afero.NewMemMapFs(), true, false, false)
		errorFromFunction := make(chan error)
		go func() {
			errorFromFunction <- b.runBuildSteps(ctx)
		}()

		for idx := range tc.buildRequest.Steps {

			if len(tc.wantCommands) != 0 {
				args := <-stepArgs
				if tc.wantCommands[idx] != args {
					t.Errorf("%s: Commands didn't match!\n===Want:\n%s\n===Got:\n%s", tc.name, tc.wantCommands[idx], args)
				}
			} else {
				<-stepArgs
			}
		}

		err := <-errorFromFunction
		if !reflect.DeepEqual(err, tc.wantErr) {
			t.Errorf("%s: Wanted error %q, but got %q", tc.name, tc.wantErr, err)
		}
	}
}

func TestPushImages(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	testCases := []struct {
		name             string
		buildRequest     pb.Build
		wantErr          error
		wantCommands     []string
		remotePushesFail bool
	}{{
		name:             "TestPushImages",
		buildRequest:     commonBuildRequest,
		remotePushesFail: false,
		wantCommands: []string{
			"docker run --name cloudbuild_docker_push_" + uuidRegex + " --rm --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock gcr.io/cloud-builders/docker push gcr.io/build-output-tag-1",
			"docker run --name cloudbuild_docker_push_" + uuidRegex + " --rm --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock gcr.io/cloud-builders/docker push gcr.io/build-output-tag-2",
			"docker run --name cloudbuild_docker_push_" + uuidRegex + " --rm --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock gcr.io/cloud-builders/docker push gcr.io/build-output-tag-no-digest",
		},
	}, {
		name:             "TestPushImagesFail",
		buildRequest:     commonBuildRequest,
		remotePushesFail: true,
		wantErr:          errors.New(`error pushing image "gcr.io/build-output-tag-1": exit status 1`),
	}}
	for _, tc := range testCases {
		r := newMockRunner(t, tc.name)
		b := New(r, tc.buildRequest, mockTokenSource(), nopBuildLogger{}, nopEventLogger{}, "", afero.NewMemMapFs(), true, true, false)
		r.remotePushesFail = tc.remotePushesFail
		gotErr := b.pushImages(ctx)
		if !reflect.DeepEqual(gotErr, tc.wantErr) {
			t.Errorf("%s: Wanted error %q, but got %q", tc.name, tc.wantErr, gotErr)
		}
		if tc.wantCommands != nil {
			got := strings.Join(r.commands, "\n")
			want := strings.Join(tc.wantCommands, "\n")
			if match, _ := regexp.MatchString(want, got); !match {
				t.Errorf("%s: Commands didn't match!\n===Want:\n%s\n===Got:\n%s", tc.name, want, got)
			}
			// Validate side-effects of pushing docker images.
			wantDigest := "sha256:deadb33f000deadb33f0000123456789abcdeffffffffffffff"
			b.Timing = TimingInfo{}
			summary := b.Summary()
			wantSummary := BuildSummary{
				BuiltImages: []BuiltImage{{
					Name:   "gcr.io/build-output-tag-1",
					Digest: wantDigest,
				}, {
					Name:   "gcr.io/build-output-tag-1:latest",
					Digest: wantDigest,
				}, {
					Name:   "gcr.io/build-output-tag-2",
					Digest: wantDigest,
				}, {
					Name:   "gcr.io/build-output-tag-2:latest",
					Digest: wantDigest,
				}},
				// gcr.io/build-output-tag-no-digest doesn't show up at all b/c it has no digest!
				BuildStepImages: []string{"", ""},
				Timing:          TimingInfo{},
				StepStatus:      []pb.Build_Status{},
			}
			if !reflect.DeepEqual(summary, wantSummary) {
				t.Errorf("%s: unexpected build summary:\n got %+v\nwant %+v", tc.name, summary, wantSummary)
			}
		}
	}
}

type mockGsutilHelper struct {
	bucket       string
	numArtifacts int // define num of artifacts to upload
}

func newMockGsutilHelper(bucket string, numArtifacts int) mockGsutilHelper {
	return mockGsutilHelper{
		bucket:       strings.TrimSuffix(bucket, "/"), // remove any trailing slash for consistency
		numArtifacts: numArtifacts,
	}
}

func (g mockGsutilHelper) VerifyBucket(ctx context.Context, bucket string) error {
	if bucket == g.bucket {
		return nil
	}
	return fmt.Errorf("bucket %q does not exist", bucket)
}

func (g mockGsutilHelper) UploadArtifacts(ctx context.Context, flags gsutil.DockerFlags, src, dest string) ([]*pb.ArtifactResult, error) {
	artifacts := []*pb.ArtifactResult{}
	for i := 0; i < g.numArtifacts; i++ {
		// NB: We do not care about the actual ArtifactResult contents for build_test, just the number produced.
		uuid := uuid.New() // mock unique ArtifactResult
		artifacts = append(artifacts, &pb.ArtifactResult{
			Location: g.bucket + "/artifact-" + uuid,
			FileHash: []*pb.FileHashes{{
				FileHash: []*pb.Hash{{Type: pb.Hash_MD5, Value: []byte("md5" + uuid)}}},
			},
		})
	}

	return artifacts, nil
}

func (g mockGsutilHelper) UploadArtifactsManifest(ctx context.Context, flags gsutil.DockerFlags, manifest, bucket string, results []*pb.ArtifactResult) (string, error) {
	b := strings.TrimSuffix(bucket, "/") // remove any trailing forward slash
	return strings.Join([]string{b, manifest}, "/"), nil
}

// TestPushArtifacts checks that after artifacts are uploaded, an ArtifactsInfo object is assigned to the build's artifacts field.
func TestPushArtifacts(t *testing.T) {
	ctx := context.Background()
	newUUID = func() string { return "someuuid" }
	defer func() { newUUID = uuid.New }()
	buildID := "buildID"

	testCases := []struct {
		name              string
		buildRequest      pb.Build
		gsutilHelper      mockGsutilHelper
		local             bool
		wantArtifactsInfo ArtifactsInfo
		wantErr           bool
	}{{
		name: "SuccessPushArtifacts",
		buildRequest: pb.Build{
			Id: buildID,
			Artifacts: &pb.Artifacts{
				Objects: &pb.Artifacts_ArtifactObjects{
					Location: "gs://some-bucket",
					Paths:    []string{"artifact.txt"},
				},
			},
		},
		gsutilHelper: newMockGsutilHelper("gs://some-bucket", 1),
		wantArtifactsInfo: ArtifactsInfo{
			ArtifactManifest: "gs://some-bucket/artifacts-" + buildID + ".json",
			NumArtifacts:     1,
		},
	}, {
		// If the push is local, there will be no build ID. The manifest will have a different format
		// and will be assigned some unique identifying string.
		name: "SuccessPushArtifactsLocal",
		buildRequest: pb.Build{
			Artifacts: &pb.Artifacts{
				Objects: &pb.Artifacts_ArtifactObjects{
					Location: "gs://some-bucket",
					Paths:    []string{"artifact.txt"},
				},
			},
		},
		gsutilHelper: newMockGsutilHelper("gs://some-bucket", 1),
		local:        true,
		wantArtifactsInfo: ArtifactsInfo{
			ArtifactManifest: "gs://some-bucket/artifacts-localbuild_" + newUUID() + ".json",
			NumArtifacts:     1,
		},
	}, {
		name:         "SuccessNoArtifactsField",
		buildRequest: commonBuildRequest,
		gsutilHelper: newMockGsutilHelper("gs://some-bucket", 0),
	}, {
		name: "SuccessNoObjectsField",
		buildRequest: pb.Build{
			Artifacts: &pb.Artifacts{},
		},
		gsutilHelper: newMockGsutilHelper("gs://some-bucket", 0),
	}, {
		name: "ErrorBucketDoesNotExist",
		buildRequest: pb.Build{
			Artifacts: &pb.Artifacts{
				Objects: &pb.Artifacts_ArtifactObjects{
					Location: "gs://idonotexist",
					Paths:    []string{"artifact.txt"},
				},
			},
		},
		gsutilHelper: newMockGsutilHelper("gs://some-bucket", 0),
		wantErr:      true,
	}}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := newMockRunner(t, tc.name)

			local := tc.local
			b := New(r, tc.buildRequest, mockTokenSource(), nopBuildLogger{}, nopEventLogger{}, "", afero.NewMemMapFs(), local, true, false)
			b.gsutilHelper = tc.gsutilHelper

			err := b.pushArtifacts(ctx)
			if tc.wantErr {
				if err != nil {
					return
				}
				t.Fatal("got nil error, want error")
			}
			if err != nil {
				t.Fatalf("pushArtifacts(): err = %v", err)
			}

			if b.artifacts.ArtifactManifest != tc.wantArtifactsInfo.ArtifactManifest {
				t.Errorf("got b.artifacts.ArtifactManifest = %s, want %s", b.artifacts.ArtifactManifest, tc.wantArtifactsInfo.ArtifactManifest)
			}
			if b.artifacts.NumArtifacts != tc.wantArtifactsInfo.NumArtifacts {
				t.Errorf("got b.artifacts.ArtifactManifest = %s, want %s", b.artifacts.NumArtifacts, tc.wantArtifactsInfo.NumArtifacts)
			}
		})
	}
}

func TestPushArtifactsTiming(t *testing.T) {
	timeNow = fakeTimeNow
	defer func() { timeNow = time.Now }()
	ctx := context.Background()

	testCases := []struct {
		name               string
		buildRequest       pb.Build
		wantArtifactTiming bool
	}{{
		name: "NoArtifacts",
		buildRequest: pb.Build{
			Steps: []*pb.BuildStep{{
				Name: "gcr.io/my-project/my-compiler",
			}},
		},
	}, {
		name: "HasArtifacts",
		buildRequest: pb.Build{
			Steps: []*pb.BuildStep{{
				Name: "gcr.io/my-project/my-compiler",
			}},
			Artifacts: &pb.Artifacts{
				Objects: &pb.Artifacts_ArtifactObjects{
					Location: "gs://some-bucket",
					Paths:    []string{"artifact.txt"},
				}},
		},
		wantArtifactTiming: true,
	}}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := newMockRunner(t, tc.name)
			b := New(r, tc.buildRequest, mockTokenSource(), nopBuildLogger{}, nopEventLogger{}, "", afero.NewMemMapFs(), true, true, false)
			b.gsutilHelper = newMockGsutilHelper("gs://some-bucket", 0)
			if err := b.pushArtifacts(ctx); err != nil {
				t.Fatalf("b.pushArtifacts() = %v", err)
			}

			// Ensure b.Timing.ArtifactsPushes exists if we expect it to
			artifactTiming := b.Timing.ArtifactsPushes
			if tc.wantArtifactTiming && artifactTiming == nil {
				t.Fatalf("got b.Timing.ArtifactPushes = nil, want TimeSpan value")
			}
			if !tc.wantArtifactTiming && artifactTiming != nil {
				t.Fatalf("got b.Timing.ArtifactPushes = %+v, want nil", artifactTiming)
			}
			if artifactTiming != nil && !isEndTimeAfterStartTime(artifactTiming) {
				t.Errorf("invalid TimeSpan value: got %+v\nwant TimeSpan EndTime to occur after StartTime", artifactTiming)
			}
		})
	}
}

func TestPushAllTiming(t *testing.T) {
	ctx := context.Background()
	newUUID = func() string { return "someuuid" }
	defer func() { newUUID = uuid.New }()

	testCases := []struct {
		name         string
		buildRequest pb.Build
		hasPushTotal bool
	}{{
		name: "Push",
		buildRequest: pb.Build{
			Steps: []*pb.BuildStep{{
				Name: "gcr.io/my-project/my-compiler",
			}},
			Images: []string{"gcr.io/build-output-tag-1", "gcr.io/build-output-tag-2", "gcr.io/build-output-tag-no-digest"},
			Artifacts: &pb.Artifacts{
				Objects: &pb.Artifacts_ArtifactObjects{
					Location: "gs://some-bucket",
					Paths:    []string{"artifact.txt"},
				}},
		},
		hasPushTotal: true,
	}, {
		name: "NoPush",
		buildRequest: pb.Build{
			Steps: []*pb.BuildStep{{
				Name: "gcr.io/my-project/my-compiler",
			}},
		},
	}}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := newMockRunner(t, tc.name)
			b := New(r, tc.buildRequest, mockTokenSource(), nopBuildLogger{}, nopEventLogger{}, "", afero.NewMemMapFs(), true, true, false)
			b.gsutilHelper = newMockGsutilHelper("gs://some-bucket", 1)
			if err := b.pushAll(ctx); err != nil {
				t.Fatal(err)
			}

			pushTotal := b.Timing.PushTotal
			if !tc.hasPushTotal {
				if pushTotal != nil {
					t.Fatalf("got pushTotal = %+v, want nil", pushTotal)
				}
				return // desired behavior
			}
			if tc.hasPushTotal && pushTotal == nil {
				t.Fatal("did not get pushTotal")
			}
			if pushTotal != nil && pushTotal.End.IsZero() {
				t.Errorf("got b.Timing.PushTotal.End.IsZero() = true, want false")
			}
			if !isEndTimeAfterStartTime(pushTotal) {
				t.Errorf("unexpected push total time:\n got %+v\nwant TimeSpan EndTime to occur after StartTime", pushTotal)
			}
		})
	}

}

var timeSecs int64
var mu sync.Mutex

var fakeTimeNow = func() time.Time {
	mu.Lock()
	defer mu.Unlock()
	timeSecs++
	return time.Unix(timeSecs, 0)
}

func isEndTimeAfterStartTime(ts *TimeSpan) bool {
	return ts.End.Sub(ts.Start) > 0
}

func getStepIndex(id string, buildSteps []*pb.BuildStep) int {
	for i, bs := range buildSteps {
		if bs.Id == id {
			return i
		}
	}
	return -1
}

func TestBuildTiming(t *testing.T) {
	timeNow = fakeTimeNow
	defer func() { timeNow = time.Now }()
	ctx := context.Background()

	testCases := []struct {
		name         string
		buildRequest pb.Build
		hasError     bool
		// stepTimings is only defined when an error is expected, since not all the steps will execute.
		stepTimings int
	}{{
		name:         "BuildTotalTiming",
		buildRequest: commonBuildRequest,
	}, {
		name: "PushImagesTimingZeroSteps",
	}, {
		name: "PushImagesTimingFiveSteps",
		buildRequest: pb.Build{
			Steps: []*pb.BuildStep{{
				Name: "gcr.io/step-zero",
			}, {
				Name: "gcr.io/step-one",
			}, {
				Name: "gcr.io/step-two",
			}, {
				Name: "gcr.io/step-three",
			}, {
				Name: "gcr.io/step-four",
			}},
		},
	}, {
		name: "BuildImagesTimingParallelSteps",
		buildRequest: pb.Build{
			Steps: []*pb.BuildStep{{
				Name:    "gcr.io/step-zero",
				Id:      "A",
				WaitFor: []string{StartStep},
			}, {
				Name:    "gcr.io/step-one",
				Id:      "B",
				WaitFor: []string{"A"},
			}, {
				Name:    "gcr.io/step-two",
				Id:      "C",
				WaitFor: []string{"A"},
			}, {
				Name:    "gcr.io/step-three",
				Id:      "D",
				WaitFor: []string{"A"},
			}, {
				Name:    "gcr.io/step-four",
				Id:      "E",
				WaitFor: []string{"D"},
			}},
		},
	}, {
		name: "BuildStepError",
		buildRequest: pb.Build{
			Steps: []*pb.BuildStep{{
				Name: "gcr.io/step-zero",
			}, {
				Name: "gcr.io/step-one",
			}, {
				Name: "iamanerror", // failed step will have timing
			}, {
				Name: "gcr.io/step-three", // this will not execute
			}},
		},
		hasError:    true,
		stepTimings: 3,
	}}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := newMockRunner(t, tc.name)
			r.dockerRunHandler = func([]string, io.Writer, io.Writer) error {
				r.mu.Lock()
				r.localImages["gcr.io/build-output-tag-1"] = true
				r.localImages["gcr.io/build-output-tag-no-digest"] = true
				r.localImages["gcr.io/build-output-tag-2"] = true
				r.mu.Unlock()
				return nil
			}
			b := New(r, tc.buildRequest, mockTokenSource(), nopBuildLogger{}, nopEventLogger{}, "", afero.NewMemMapFs(), true, false, false)
			b.runBuildSteps(ctx)

			buildStepTimes := b.Timing.BuildSteps

			// Verify build step and total build timings properly captured.
			if b.Timing.BuildTotal == nil {
				t.Errorf("got build.Timing.BuildTotal = nil, want TimeSpan value")
			}
			if !isEndTimeAfterStartTime(b.Timing.BuildTotal) {
				t.Errorf("unexpected build total time:\n got %+v\nwant TimeSpan EndTime to occur after StartTime", b.Timing.BuildTotal)
			}

			expectedStepTimings := len(tc.buildRequest.Steps)
			if tc.hasError {
				// If there as an error, not all the build steps will be timed,
				// so test case value for expected number of steps.
				expectedStepTimings = tc.stepTimings
			}
			stepTimings := 0
			for _, bs := range buildStepTimes {
				if bs == nil {
					continue // do not evaluate nil timings
				}
				stepTimings++
				if !isEndTimeAfterStartTime(bs) {
					t.Errorf("unexpected build step time:\n got %+v\nwant TimeSpan EndTime to occur after StartTime", bs)
				}
			}
			if stepTimings != expectedStepTimings {
				t.Errorf("unexpected number of build step times:\n got %d\nwant %d", stepTimings, expectedStepTimings)
			}

			if tc.hasError {
				// If there is an error, just verify the order of the step timings that exist.
				
				// We do not need to check the WaitFor field below because the error test case has consecutive steps.
				prevStep := buildStepTimes[0]
				for i := 1; i < len(buildStepTimes); i++ {
					if buildStepTimes[i] == nil {
						continue
					}
					if buildStepTimes[i].Start.Before(prevStep.End) {
						t.Errorf("build step %d started before step %d ended", i, i-1)
					}
					prevStep = buildStepTimes[i]
				}
				return
			}
			// Otherwise, verify the order of all step timings.
			buildSteps := tc.buildRequest.Steps
			for i, bs := range buildSteps {
				if bs.WaitFor != nil {
					for _, wf := range bs.WaitFor {
						if wf == "-" {
							continue
						}
						wfIndex := getStepIndex(wf, buildSteps)
						if wfIndex == -1 {
							t.Errorf("build step id %s not present in test case's build request", wf)
							continue
						}
						end := buildStepTimes[wfIndex].End // end time of the step with ID "wf"
						if buildStepTimes[i].Start.Before(end) {
							t.Errorf(" build step %d started before step %d ended", i, wfIndex)
						}
					}
				} else {
					// If waitFor isn't defined, it's all previously defined build steps
					for step := 0; step < i; step++ {
						end := buildStepTimes[step].End // end time of the previous step
						if buildStepTimes[i].Start.Before(end) {
							t.Errorf(" build step %d started before step %d ended", i, step)
						}
					}
				}
			}
		})
	}
}

// TestBuildTimingOutOfOrder ensures the correctness of build step timings when steps execute out of order.
func TestBuildTimingOutOfOrder(t *testing.T) {
	ctx := context.Background()
	// Step args specify how long the step should sleep in milliseconds.
	testCases := []struct {
		name         string
		buildRequest pb.Build
		finishOrder  []int // order that build steps finish
		hasError     bool
	}{{
		name: "TwoStepsReverseOrder",
		buildRequest: pb.Build{
			Steps: []*pb.BuildStep{{
				Name: "gcr.io/step-zero",
				Args: []string{"sleep", "25"},
			}, {
				Name:    "gcr.io/step-one",
				Args:    []string{"sleep", "10"},
				WaitFor: []string{"-"}, // begin immediately while step 0 is running
			}},
		},
		finishOrder: []int{1, 0},
	}, {
		name: "FourStepsReverseOrder",
		buildRequest: pb.Build{
			Steps: []*pb.BuildStep{{
				Name: "gcr.io/step-zero",
				Args: []string{"sleep", "100"},
			}, {
				Name:    "gcr.io/step-one",
				Args:    []string{"sleep", "50"},
				WaitFor: []string{"-"},
			}, {
				Name:    "gcr.io/step-two",
				Args:    []string{"sleep", "25"},
				WaitFor: []string{"-"},
			}, {
				Name:    "gcr.io/step-three",
				Args:    []string{"sleep", "10"},
				WaitFor: []string{"-"},
			}},
		},
		finishOrder: []int{3, 2, 1, 0},
	}, {
		name: "MixedOrder",
		buildRequest: pb.Build{
			Steps: []*pb.BuildStep{{
				Name: "gcr.io/step-zero",
				Args: []string{"sleep", "100"},
			}, {
				Name:    "gcr.io/step-one",
				Args:    []string{"sleep", "10"},
				WaitFor: []string{"-"},
			}, {
				Name:    "gcr.io/step-two",
				Args:    []string{"sleep", "50"},
				WaitFor: []string{"-"},
			}, {
				Name:    "gcr.io/step-three",
				Args:    []string{"sleep", "25"},
				WaitFor: []string{"-"},
			}},
		},
		finishOrder: []int{1, 3, 2, 0},
	}}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := newMockRunner(t, tc.name)
			r.dockerRunHandler = func([]string, io.Writer, io.Writer) error { return nil }
			b := New(r, tc.buildRequest, mockTokenSource(), nopBuildLogger{}, nopEventLogger{}, "", afero.NewMemMapFs(), true, false, false)
			b.runBuildSteps(ctx)

			buildStepTimes := b.Timing.BuildSteps

			// Verify build total timings.
			if b.Timing.BuildTotal == nil {
				t.Errorf("got build.Timing.BuildTotal = nil, want TimeSpan value")
			}
			if !isEndTimeAfterStartTime(b.Timing.BuildTotal) {
				t.Errorf("unexpected build total time:\n got %+v\nwant TimeSpan EndTime to occur after StartTime", b.Timing.BuildTotal)
			}

			// Verify build step timings.
			if len(buildStepTimes) != len(tc.buildRequest.Steps) {
				t.Errorf("unexpected number of build step times:\n got %d\nwant %d", len(buildStepTimes), len(tc.buildRequest.Steps))
			}
			for _, bs := range buildStepTimes {
				if bs == nil {
					t.Error("unexpected build step time:\n got nil")
				}
				if !isEndTimeAfterStartTime(bs) {
					t.Errorf("unexpected build step time:\n got %+v\nwant TimeSpan EndTime to occur after StartTime", bs)
				}
			}
			// Verify that build steps ended in the right order.
			prevIdx := tc.finishOrder[0]
			for _, idx := range tc.finishOrder[1:] {
				if buildStepTimes[idx].End.Before(buildStepTimes[prevIdx].End) {
					t.Errorf("build step %d ended before step %d ended", idx, prevIdx)
				}
				prevIdx = idx
			}
		})
	}
}

func TestPushImagesTiming(t *testing.T) {
	
	timeNow = fakeTimeNow
	defer func() { timeNow = time.Now }()
	ctx := context.Background()

	testCases := []struct {
		name               string
		buildRequest       pb.Build
		push               bool
		wantImagePushTimes int
		// wantFirstPushTimeOnly is true in cases where we push more than one image with the same digest.
		// In those cases, only the first pushed image timing should be stored. After an image is pushed,
		// additional pushes of the same image will take negligible time.
		wantFirstPushTimeOnly bool
	}{{
		name:               "OneImage",
		buildRequest:       commonBuildRequest,
		push:               true,
		wantImagePushTimes: 1,
	}, {
		name: "NoImage",
		buildRequest: pb.Build{
			Steps: []*pb.BuildStep{{
				Name: "gcr.io/my-project/my-compiler",
			}},
		},
		push:               false,
		wantImagePushTimes: 0,
	}, {
		name: "ThreeImages",
		buildRequest: pb.Build{
			Steps: []*pb.BuildStep{{
				Name: "gcr.io/step-one",
			}, {
				Name: "gcr.io/step-two",
			}, {
				Name: "gcr.io/step-three",
			}},
			Images: []string{"gcr.io/some-image-1", "gcr.io/some-image-2", "gcr.io/some-image-3"},
		},
		push:               true,
		wantImagePushTimes: 3,
	}, {
		name: "ParallelStepsThreeImages",
		buildRequest: pb.Build{
			Steps: []*pb.BuildStep{{
				Name:    "gcr.io/my-project/my-builder",
				Id:      "A",
				WaitFor: []string{StartStep},
			}, {
				Id:      "B",
				WaitFor: []string{"A"},
			}, {
				Id:      "C",
				WaitFor: []string{"A"},
			}, {
				Id:      "D",
				WaitFor: []string{"A"},
			}},
			Images: []string{"gcr.io/some-image-1", "gcr.io/some-image-2", "gcr.io/some-image-3"},
		},
		push:               true,
		wantImagePushTimes: 3,
	}, {
		// Push three images with the same image digest.
		name: "SameImageDigest",
		buildRequest: pb.Build{
			Steps:  []*pb.BuildStep{{Name: "gcr.io/step-one"}},
			Images: []string{"gcr.io/build-output-tag-1", "gcr.io/build-output-tag-2", "gcr.io/build-output-tag-no-digest"},
		},
		push:                  true,
		wantImagePushTimes:    1,
		wantFirstPushTimeOnly: true,
	}}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := newMockRunner(t, tc.name)

			// We record this for the test case where tc.wantFirstPushTimeOnly is true. Since fakeTimeNow increments the second counter after each call,
			// the first image push start time should start 1 second after pushStart.
			pushStart := timeNow()

			b := New(r, tc.buildRequest, mockTokenSource(), nopBuildLogger{}, nopEventLogger{}, "", afero.NewMemMapFs(), true, true, false)
			b.pushImages(ctx)
			imagePushes := b.Timing.ImagePushes
			for _, pushTime := range imagePushes {
				if !isEndTimeAfterStartTime(pushTime) {
					t.Errorf("unexpected push total time:\n got %+v\nwant TimeSpan EndTime to occur after StartTime", pushTime)
				}
			}
			if len(imagePushes) == tc.wantImagePushTimes {
				for _, d := range b.imageDigests {
					ts, ok := imagePushes[d.digest]
					if !ok {
						t.Errorf("timing for image push not present:\nwanted time for %v but no such key", d)
					}
					if tc.wantFirstPushTimeOnly {
						if diff := ts.Start.Unix() - pushStart.Unix(); diff != 1 {
							t.Errorf("timing for image %s is not the timing of the first push:\ngot image push timing starts %d seconds after push total start time, want 1 second", d.tag, diff)
						}
					}
				}
			} else {
				t.Errorf("unexpected number of image push times:\n got %v\nwant %v", len(imagePushes), tc.wantImagePushTimes)
			}
		})
	}
}

func TestBuildStepConcurrency(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	req := pb.Build{
		// This build is set up such that if build steps don't run concurrently,
		// the build will time out. Specifically, if A were to run first and alone,
		// it would timeout waiting for B to run.  If B were to run first and
		// alone, it would timeout waiting for A to run. Thus A and B must run
		// concurrently for this build to not time out.
		//
		// See comments on type fakeRunner (below) which document how the steps
		// signal each other.
		Steps: []*pb.BuildStep{{
			Name: "A",
			// Wait for nothing; free B to run by closing aIsRunning, then wait for bIsRunning.
			Args: []string{"", "aIsRunning", "bIsRunning"},
		}, {
			Name: "B",
			// Start concurrently with A.
			WaitFor: []string{StartStep},
			// Block until aIsRunning, then free A to run by closing bIsRunning.
			Args: []string{"aIsRunning", "bIsRunning"},
		}},
	}

	r := &fakeRunner{channels: make(map[string]chan struct{})}
	// Fill in the channels.
	for _, s := range req.Steps {
		for _, chName := range s.Args {
			if chName == "" {
				continue
			}
			if _, ok := r.channels[chName]; ok {
				continue
			}
			r.channels[chName] = make(chan struct{})
		}
	}

	// Run the build.
	b := New(r, req, mockTokenSource(), nopBuildLogger{}, nopEventLogger{}, "", afero.NewMemMapFs(), true, false, false)
	ret := make(chan error)
	go func() {
		ret <- b.runBuildSteps(ctx)
	}()

	// Give the build 10s to complete; it should take <1s.
	timeout := time.NewTimer(time.Second * 10)
	defer timeout.Stop()

	select {
	case err := <-ret:
		if err != nil {
			t.Fatalf("Failed: %v", err)
		}
	case <-timeout.C:
		t.Fatal("Timed out.")
	}
}

// fakeRunner is a mocked runner.Runner that is useful for testing build step concurrency.
//
// The args passed to "docker run" -- the args from the build steps" -- will be
// used to identify channels in the channels map so that the build steps can
// signal each other when they start and finish.
type fakeRunner struct {
	runner.Runner
	channels map[string]chan struct{}
}

// Run fakes a build step execution. All commands are ignored except for
// "docker run" (which is the actual execution of the build step).
// We use the args passed to the "build step" to chose signal channels from the
// channels map.  Specifically, we wait for the first channel given to close,
// then close the next channel then repeat until we've gone through all the
// "args".
func (f *fakeRunner) Run(ctx context.Context, args []string, _ io.Reader, _, _ io.Writer, _ string) error {
	// The "+1" is for the name of the container which is appended to the
	// dockerRunArgs base command.
	b := New(nil, pb.Build{}, nil, nopBuildLogger{}, nopEventLogger{}, "", afero.NewMemMapFs(), true, false, false)
	argCount := len(b.dockerRunArgs("", "", 0)) + 1
	switch {
	case !startsWith(args, "docker", "run"):
		// It's not a "docker run" invocation; do nothing.
		return nil
		// For a "docker run", we expect at least 1 additional arg -- the name of a
		// channel -- as part of the args slice.
	case len(args) < argCount+1:
		return fmt.Errorf("expected >=%d args; found %d: %q", argCount+1, len(args), args)
	}

	// Args 0-(argCount-2) are "docker run" + flags; see build.dockerRunArgs().
	// Arg argCount-1 is the name of the docker container to run, a.k.a. the name of the build step.
	stepName := args[argCount-1]

	// Args argCount... are the args passed to the "docker run foo" command,
	// which in our case are channel names.
	for i, chName := range args[argCount:] {
		if chName == "" {
			log.Printf("Step %q ignoring blank channel in arg %d.", stepName, i)
			continue
		}

		ch, ok := f.channels[chName]
		if !ok {
			return fmt.Errorf("step %q referenced nonexistent channel %q", stepName, chName)
		}

		switch i % 2 {
		case 0:
			<-ch

		case 1:
			close(ch)
		}
	}
	log.Printf("Step %q finished successfully.", stepName)
	return nil
}

func (f *fakeRunner) Clean() error {
	return nil
}

// This function helps generate test "docker run" arguments.
func dockerRunString(idx int) string {
	return dockerRunInStepDir(idx, "")
}

func dockerRunInStepDir(idx int, stepDir string) string {
	b := New(nil, pb.Build{}, nil, nopBuildLogger{}, nopEventLogger{}, "", afero.NewMemMapFs(), true, false, false)
	dockerRunArg := b.dockerRunArgs(stepDir, "", idx)
	return strings.Join(dockerRunArg, " ")
}

func (f *fakeRunner) MkdirAll(_ string) error {
	return nil
}

func (f *fakeRunner) WriteFile(_, _ string) error {
	return nil
}

func fakeTimeSpan() *TimeSpan {
	return &TimeSpan{fakeTimeNow(), fakeTimeNow()}
}

func TestSummary(t *testing.T) {
	b := New(nil, pb.Build{}, mockTokenSource(), nopBuildLogger{}, nopEventLogger{}, "", afero.NewMemMapFs(), true, true, false)

	wantBuildStatus := StatusDone
	wantStepStatus := []pb.Build_Status{pb.Build_SUCCESS, pb.Build_SUCCESS}
	wantBuiltImages := []BuiltImage{{Name: "tag", Digest: "digest"}}
	imageDigests := []imageDigest{{"tag", "digest"}}
	wantBuildStepImages := []string{"digest1", "digest2"}
	wantTiming := TimingInfo{
		BuildSteps:      []*TimeSpan{fakeTimeSpan()},
		BuildTotal:      fakeTimeSpan(),
		ImagePushes:     map[string]*TimeSpan{"someimage": fakeTimeSpan()},
		PushTotal:       fakeTimeSpan(),
		SourceTotal:     fakeTimeSpan(),
		ArtifactsPushes: fakeTimeSpan(),
	}
	wantArtifacts := ArtifactsInfo{
		ArtifactManifest: "manifest",
		NumArtifacts:     1,
	}

	b.Status = wantBuildStatus
	b.stepStatus = wantStepStatus
	b.imageDigests = imageDigests
	b.stepDigests = wantBuildStepImages
	b.Timing = wantTiming
	b.artifacts = wantArtifacts

	s := b.Summary()
	if s.Status != wantBuildStatus {
		t.Errorf("got Status = %s, want %v", s.Status, wantBuildStatus)
	}
	if !reflect.DeepEqual(s.StepStatus, wantStepStatus) {
		t.Errorf("got StepStatus = %+v, want %+v", s.StepStatus, wantStepStatus)
	}
	if !reflect.DeepEqual(s.BuiltImages, wantBuiltImages) {
		t.Errorf("got BuiltImages = %+v, want %+v", s.BuiltImages, wantBuiltImages)
	}
	if !reflect.DeepEqual(s.BuildStepImages, wantBuildStepImages) {
		t.Errorf("got BuildStepImages = %+v, want %+v", s.BuildStepImages, wantBuildStepImages)
	}
	if !reflect.DeepEqual(s.Timing, wantTiming) {
		t.Errorf("got Timing = %+v, want Timing = %+v", s.Timing, wantTiming)
	}
	if !reflect.DeepEqual(s.Artifacts, wantArtifacts) {
		t.Errorf("got Artifacts = %+v, want %+v", s.Artifacts, wantArtifacts)
	}
}

func TestFindStatus(t *testing.T) {
	t.Parallel()
	tc := []struct{ in, want string }{
		{"failed some other way", ""},
		{"failed with http status: 300: mysterious 300 error", "300"},
		{"failed with http status: 403: permission denied", "403"},
		{"", ""},
	}

	for _, c := range tc {
		if got := findStatus(c.in); got != c.want {
			t.Errorf("%q: want %q; got %q", c.in, c.want, got)
		}
	}
}

func TestErrorCollection(t *testing.T) {
	t.Parallel()
	outputs := []string{
		"DNS stinks! no such host was found",
		"Firewalled: network is unreachable by you",
		"Don't you hate seeing 500 Internal Server Error?",
		"Exactly what is a 502 Bad Gateway?",
		"Uh oh, token auth attempt for registry failed",
		"Blood on your hands: net/http: TLS handshake timeout",
		"failed with some unknown error",
		"got an http status: 403: permission denied",
		"got an http status: 300: it's a mystery to me",
		"got another http status: 300: it's a double mystery",
	}
	b := New(nil, pb.Build{}, nil, nopBuildLogger{}, nopEventLogger{}, "", afero.NewMemMapFs(), true, false, false)
	for _, o := range outputs {
		b.detectPushFailure(o)
	}
	want := map[string]int64{
		"dnsFailure":         1,
		"networkUnreachable": 1,
		"500":                1,
		"502":                1,
		"authError":          1,
		"tlsTimeout":         1,
		"300":                2,
		"403":                1,
	}
	if !reflect.DeepEqual(b.GCRErrors, want) {
		t.Errorf("want %v; got %v", want, b.GCRErrors)
	}
}

func TestEntrypoint(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	testCases := []struct {
		name         string
		buildRequest pb.Build
		wantErr      error
		wantCommands []string
	}{{
		name: "TestWithoutEntrypoint",
		buildRequest: pb.Build{
			Steps: []*pb.BuildStep{{
				Name: "gcr.io/my-project/my-builder",
				Env:  []string{"FOO=bar", "BAZ=buz"},
				Args: []string{"a", "b", "c"},
				Dir:  "foo/bar/../baz",
			}},
		},
		wantCommands: []string{
			dockerRunInStepDir(0, "foo/baz") +
				" --env FOO=bar" +
				" --env BAZ=buz" +
				" gcr.io/my-project/my-builder a b c",
		},
	}, {
		name: "TestWithEntrypoint",
		buildRequest: pb.Build{
			Steps: []*pb.BuildStep{{
				Name:       "gcr.io/my-project/my-builder",
				Env:        []string{"FOO=bar", "BAZ=buz"},
				Args:       []string{"a", "b", "c"},
				Dir:        "foo/bar/../baz",
				Entrypoint: "bash",
			}},
		},
		wantCommands: []string{
			dockerRunInStepDir(0, "foo/baz") +
				" --env FOO=bar" +
				" --env BAZ=buz" +
				" --entrypoint bash" +
				" gcr.io/my-project/my-builder a b c",
		},
	}}
	for _, tc := range testCases {
		stepArgs := make(chan string)
		r := newMockRunner(t, tc.name)
		r.dockerRunHandler = func(args []string, _, _ io.Writer) error {
			stepArgs <- strings.Join(args, " ")
			return nil
		}
		b := New(r, tc.buildRequest, mockTokenSource(), nopBuildLogger{}, nopEventLogger{}, "", afero.NewMemMapFs(), true, false, false)
		errorFromFunction := make(chan error)
		go func() {
			errorFromFunction <- b.runBuildSteps(ctx)
		}()

		for idx := range tc.buildRequest.Steps {
			if len(tc.wantCommands) != 0 {
				args := <-stepArgs
				if tc.wantCommands[idx] != args {
					t.Errorf("%s: Commands didn't match!\n===Want:\n%s\n===Got:\n%s", tc.name, tc.wantCommands[idx], args)
				}
			} else {
				<-stepArgs
			}
		}

		err := <-errorFromFunction
		if !reflect.DeepEqual(err, tc.wantErr) {
			t.Errorf("%s: Wanted error %q, but got %q", tc.name, tc.wantErr, err)
		}
	}
}

func TestStripTagDigest(t *testing.T) {
	tcs := []struct {
		in, out string
	}{{
		in:  "gcr.io/foo/bar@sha256:123abc",
		out: "gcr.io/foo/bar",
	}, {
		in:  "gcr.io/foo/bar:123abc",
		out: "gcr.io/foo/bar",
	}, {
		in:  "gcr.io/foo/bar",
		out: "gcr.io/foo/bar",
	}}
	for _, c := range tcs {
		got := stripTagDigest(c.in)
		if got != c.out {
			t.Errorf("For %q: got %q, want %q", c.in, got, c.out)
		}
	}
}

func TestSecrets(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	kmsKeyName := "projects/my-project/locations/global/keyRings/my-key-ring/cryptoKeys/my-crypto-key"
	for _, c := range []struct {
		name        string
		plaintext   string
		kmsErr      error
		wantErr     error
		wantCommand string
	}{{
		name: "Happy case",
		wantCommand: dockerRunInStepDir(0, "") +
			" --env MY_SECRET=sup3rs3kr1t" +
			" gcr.io/my-project/my-builder",
		plaintext: base64.StdEncoding.EncodeToString([]byte("sup3rs3kr1t")),
		kmsErr:    nil,
	}, {
		name:    "KMS returns error",
		kmsErr:  errors.New("kms failure"),
		wantErr: fmt.Errorf(`build step 0 "gcr.io/my-project/my-builder" failed: Failed to decrypt "MY_SECRET" using key %q: kms failure`, kmsKeyName),
	}, {
		name:      "KMS returns non-base64 response",
		plaintext: "This is not valid base64!",
		kmsErr:    nil,
		wantErr:   fmt.Errorf(`build step 0 "gcr.io/my-project/my-builder" failed: Plaintext was not base64-decodeable: illegal base64 data at input byte 4`),
	}} {
		// All tests use the same build request.
		buildRequest := pb.Build{
			Steps: []*pb.BuildStep{{
				Name:      "gcr.io/my-project/my-builder",
				SecretEnv: []string{"MY_SECRET"},
			}},
			Secrets: []*pb.Secret{{
				KmsKeyName: kmsKeyName,
				SecretEnv: map[string][]byte{
					"MY_SECRET": []byte("this is encrypted"),
				},
			}},
		}

		var gotCommand string
		r := newMockRunner(t, c.name)
		r.dockerRunHandler = func(args []string, _, _ io.Writer) error {
			gotCommand = strings.Join(args, " ")
			return nil
		}
		b := New(r, buildRequest, mockTokenSource(), nopBuildLogger{}, nopEventLogger{}, "", afero.NewMemMapFs(), true, false, false)
		b.kms = fakeKMS{
			plaintext: c.plaintext,
			err:       c.kmsErr,
		}

		if err := b.runBuildSteps(ctx); !reflect.DeepEqual(err, c.wantErr) {
			t.Errorf("%s: Unexpected error\n got %v\nwant %v", c.name, err, c.wantErr)
		}
		if !reflect.DeepEqual(gotCommand, c.wantCommand) {
			t.Errorf("%s: Unexpected command\n got %s\nwant: %s", c.name, c.wantCommand, gotCommand)
		}
	}
}

type fakeKMS struct {
	plaintext string
	err       error
}

func (k fakeKMS) Decrypt(_, enc string) (string, error) {
	if _, err := base64.StdEncoding.DecodeString(enc); err != nil {
		return "", err
	}
	return k.plaintext, k.err
}

func TestPushDigestScraping(t *testing.T) {
	cases := []struct {
		desc     string
		output   string
		expected []imageDigest
		err      string
	}{{
		// When you 'docker push foo', and "foo:x", "foo:y" etc are available, all "foo:*" images are pushed (rather than just :latest).
		desc: "multi push from untagged",
		output: `
The push refers to a repository [gcr.io/cloud-builders/docker]
8f88e1f321d2: Preparing
e14577d2cac5: Preparing
e8829d5bbd2c: Preparing
674ce3c5d814: Preparing
308b39a73046: Preparing
638903ee8579: Preparing
638903ee8579: Waiting
e8829d5bbd2c: Mounted from cloud-builders/bazel
674ce3c5d814: Mounted from cloud-builders/bazel
308b39a73046: Mounted from cloud-builders/bazel
e14577d2cac5: Mounted from cloud-builders/bazel
638903ee8579: Mounted from cloud-builders/bazel
8f88e1f321d2: Pushed
1.12.6: digest: sha256:22c754d23e8461f6992e900290f4a146fda90eaf89b0cbdbdfd3aaa503dad4f6 size: 1571
ea04be27024e: Preparing
e14577d2cac5: Preparing
e8829d5bbd2c: Preparing
674ce3c5d814: Preparing
308b39a73046: Preparing
638903ee8579: Preparing
e14577d2cac5: Layer already exists
e8829d5bbd2c: Layer already exists
674ce3c5d814: Layer already exists
308b39a73046: Layer already exists
638903ee8579: Waiting
638903ee8579: Layer already exists
ea04be27024e: Pushed
1.9.1: digest: sha256:16c183bac00b282e420fbc6d3f3bf56f9ae18d85c36c4f8850c951ea49ca5dc1 size: 1571
8f88e1f321d2: Preparing
e14577d2cac5: Preparing
e8829d5bbd2c: Preparing
674ce3c5d814: Preparing
308b39a73046: Preparing
638903ee8579: Preparing
8f88e1f321d2: Layer already exists
e14577d2cac5: Layer already exists
e8829d5bbd2c: Layer already exists
674ce3c5d814: Layer already exists
308b39a73046: Layer already exists
638903ee8579: Waiting
638903ee8579: Layer already exists
latest: digest: sha256:22c754d23e8461f6992e900290f4a146fda90eaf89b0cbdbdfd3aaa503dad4f6 size: 1571
`,
		expected: []imageDigest{
			{"1.12.6", "sha256:22c754d23e8461f6992e900290f4a146fda90eaf89b0cbdbdfd3aaa503dad4f6"},
			{"1.9.1", "sha256:16c183bac00b282e420fbc6d3f3bf56f9ae18d85c36c4f8850c951ea49ca5dc1"},
			{"latest", "sha256:22c754d23e8461f6992e900290f4a146fda90eaf89b0cbdbdfd3aaa503dad4f6"},
		},
	}, {
		desc: "failed push",
		output: `
The push refers to a repository [docker.io/library/ubuntu]
73e5d2de6e3e: Layer already exists
08f405d988e4: Layer already exists
511ddc11cf68: Layer already exists
a1a54d352248: Layer already exists
9d3227c1793b: Layer already exists
unauthorized: authentication required
`,
		err: "no digest in output",
	}}

	for _, c := range cases {
		got, err := scrapePushDigests(c.output)
		if err != nil {
			if c.err == err.Error() {
				continue
			}
			t.Errorf("%s: wrong error; want %q, got %q", c.desc, c.err, err)
			continue
		}
		if c.err != "" {
			t.Errorf("%s: expected error", c.desc)
			continue
		}

		if len(got) != len(c.expected) {
			t.Errorf("%s: wrong number of results; want %d, got %d", c.desc, len(c.expected), len(got))
		}
		if !reflect.DeepEqual(got, c.expected) {
			t.Errorf("%s: got\n%s\nwant\n%s", c.desc, got, c.expected)
		}
	}
}

func TestResolveDigestsForImage(t *testing.T) {
	cases := []struct {
		desc, image       string
		digests, expected []imageDigest
	}{{
		desc:    "listed untagged, pushed latest",
		image:   "foo",
		digests: []imageDigest{{"latest", "123"}},
		expected: []imageDigest{
			{"foo", "123"},
			{"foo:latest", "123"},
		},
	}, {
		desc:    "listed latest, pushed latest",
		image:   "foo:latest",
		digests: []imageDigest{{"latest", "123"}},
		expected: []imageDigest{
			{"foo", "123"},
			{"foo:latest", "123"},
		},
	}, {
		desc:  "listed untagged, pushed x and y",
		image: "foo",
		digests: []imageDigest{
			{"x", "123"},
			{"y", "456"},
		},
		expected: []imageDigest{
			{"foo:x", "123"},
			{"foo:y", "456"},
		},
	}, {
		desc:     "listed x, pushed x",
		image:    "foo:x",
		digests:  []imageDigest{{"x", "123"}},
		expected: []imageDigest{{"foo:x", "123"}},
	}}
	for _, c := range cases {
		got := resolveDigestsForImage(c.image, c.digests)
		if len(got) != len(c.expected) {
			t.Errorf("%s: wrong number of results; want %d, got %d", c.desc, len(c.expected), len(got))
		}
		if !reflect.DeepEqual(got, c.expected) {
			t.Errorf("%s: got\n%s\nwant\n%s", c.desc, got, c.expected)
		}
	}
}

func TestTimeout(t *testing.T) {
	ctx := context.Background()
	request := pb.Build{
		Steps: []*pb.BuildStep{{
			Name: "gcr.io/step-zero",
		}, {
			Name:    "gcr.io/step-one",
			Args:    []string{"sleep", "5000"}, // mockRunner will sleep this many ms.
			WaitFor: []string{"-"},
		}, {
			Name: "gcr.io/step-two",
		}},
		Timeout: &durpb.Duration{Seconds: 1}, // Build should time out.
	}
	r := newMockRunner(t, "timeout")
	r.dockerRunHandler = func([]string, io.Writer, io.Writer) error { return nil }
	b := New(r, request, mockTokenSource(), nopBuildLogger{}, nopEventLogger{}, "", afero.NewMemMapFs(), true, true, false)
	b.Start(ctx)
	<-b.Done

	want := FullStatus{BuildStatus: StatusTimeout, StepStatus: []pb.Build_Status{pb.Build_SUCCESS, pb.Build_WORKING, pb.Build_QUEUED}}
	got := b.GetStatus()
	if !reflect.DeepEqual(want, got) {
		t.Errorf("want %#v, got %#v", want, got)
	}
}

func TestCancel(t *testing.T) {
	ctx := context.Background()
	request := pb.Build{
		Steps: []*pb.BuildStep{{
			Name: "gcr.io/step-zero",
		}, {
			Name:    "gcr.io/step-one",
			Args:    []string{"sleep", "30000"}, // mockRunner will sleep this many ms.
			WaitFor: []string{"-"},
		}, {
			Name: "gcr.io/step-two",
		}},
	}
	r := newMockRunner(t, "cancel")
	r.dockerRunHandler = func([]string, io.Writer, io.Writer) error { return nil }
	b := New(r, request, mockTokenSource(), nopBuildLogger{}, nopEventLogger{}, "", afero.NewMemMapFs(), true, true, false)
	fullStatus := b.GetStatus()
	if fullStatus.BuildStatus != "" {
		t.Errorf("Build has status before Start(): %v", fullStatus)
	}

	ctx, cancel := context.WithCancel(ctx)
	go b.Start(ctx)

	// Wait for build to get started.
	const tries = 10
	const delay = time.Second
	for i := 0; i < tries; i++ {
		fullStatus = b.GetStatus()
		if fullStatus.BuildStatus != "" {
			cancel()
			break
		}
		time.Sleep(delay)
	}
	if fullStatus.BuildStatus == "" {
		t.Errorf("Build didn't start after %v.", tries*delay)
	}
	if got := ctx.Err(); got != context.Canceled {
		t.Errorf("Context not cancelled: want %q, got %q.", context.Canceled, got)
	}

	select {
	case <-b.Done:
		// Happy case.
	case <-time.After(time.Second):
		t.Errorf("b.Start() didn't finish, b.Done never unblocked.")
	}

	want := FullStatus{BuildStatus: StatusCancelled, StepStatus: []pb.Build_Status{pb.Build_SUCCESS, pb.Build_WORKING, pb.Build_QUEUED}}
	got := b.GetStatus()
	if !reflect.DeepEqual(want, got) {
		t.Errorf("want %#v, got %#v", want, got)
	}
}

func TestStart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct {
		name         string
		push         bool
		buildRequest pb.Build
		wantCommands []string
	}{{
		name: "Build and push",
		push: true,
		buildRequest: pb.Build{
			Steps: []*pb.BuildStep{{
				Name: "gcr.io/my-project/my-builder",
				Args: []string{"a"},
			}},
			Images: []string{"gcr.io/build"},
		},
		wantCommands: []string{
			"docker volume create --name homevol",
			"docker inspect gcr.io/my-project/my-builder",
			"docker run --name cloudbuild_docker_pull_" + uuidRegex + " --rm --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock gcr.io/cloud-builders/docker pull gcr.io/my-project/my-builder",
			dockerRunString(0) + " gcr.io/my-project/my-builder a",
			"docker images -q gcr.io/build",
			"docker rm -f step_0",
			"docker run --name cloudbuild_docker_push_" + uuidRegex + " --rm --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock gcr.io/cloud-builders/docker push gcr.io/build",
			"docker volume rm homevol",
		},
	}, {
		name: "Build without pushing images",
		push: false,
		buildRequest: pb.Build{
			Steps: []*pb.BuildStep{{
				Name: "gcr.io/my-project/my-builder",
				Args: []string{"a"},
			}},
			Images: []string{"gcr.io/build"},
		},
		wantCommands: []string{
			"docker volume create --name homevol",
			"docker inspect gcr.io/my-project/my-builder",
			"docker run --name cloudbuild_docker_pull_" + uuidRegex + " --rm --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock gcr.io/cloud-builders/docker pull gcr.io/my-project/my-builder",
			dockerRunString(0) + " gcr.io/my-project/my-builder a",
			"docker images -q gcr.io/build",
			"docker rm -f step_0",
			"docker volume rm homevol",
		},
	}} {
		r := newMockRunner(t, tc.name)
		r.dockerRunHandler = func(args []string, _, _ io.Writer) error {
			r.localImages["gcr.io/build"] = true
			return nil
		}
		b := New(r, tc.buildRequest, mockTokenSource(), nopBuildLogger{}, nopEventLogger{}, "", afero.NewMemMapFs(), true, tc.push, false)
		b.Start(ctx)
		<-b.Done

		if got, want := b.GetStatus().BuildStatus, StatusDone; got != want {
			t.Errorf("%s: unexpected status. got %q, want %q", tc.name, got, want)
		}

		got := strings.Join(r.commands, "\n")
		want := strings.Join(tc.wantCommands, "\n")
		if match, _ := regexp.MatchString(want, got); !match {
			t.Errorf("%s: Commands didn't match!\n===Want:\n%s\n===Got:\n%s", tc.name, want, got)
		}
	}
}

// TestUpdateDockerAccessToken tests the commands executed when setting and
// updating Docker access tokens.
func TestUpdateDockerAccessToken(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := newMockRunner(t, "TestUpdateDockerAccessToken")
	r.dockerRunHandler = func(args []string, _, _ io.Writer) error { return nil }
	b := New(r, pb.Build{}, nil, nil, nil, "", afero.NewMemMapFs(), false, false, false)

	// If UpdateDockerAccessToken is called before SetDockerAccessToken, we
	// should get an error.
	if err := b.UpdateDockerAccessToken(ctx, "INVALID"); err == nil {
		t.Errorf("Expected error when calling UpdateDockerAccessToken first, got none: %v", err)
	}

	if err := b.SetDockerAccessToken(ctx, "FIRST"); err != nil {
		t.Errorf("SetDockerAccessToken: %v", err)
	}
	if got, want := b.prevGCRAuth, base64.StdEncoding.EncodeToString([]byte("oauth2accesstoken:FIRST")); got != want {
		t.Errorf("After SetDockerAccessToken, GCR auth is %q, want %q", got, want)
	}

	if err := b.UpdateDockerAccessToken(ctx, "SECOND"); err != nil {
		t.Errorf("UpdateDockerAccessToken: %v", err)
	}
	if got, want := b.prevGCRAuth, base64.StdEncoding.EncodeToString([]byte("oauth2accesstoken:SECOND")); got != want {
		t.Errorf("After SetDockerAccessToken, GCR auth is %q, want %q", got, want)
	}

	got := strings.Join(r.commands, "\n")
	want := strings.Join([]string{
		`docker run --name cloudbuild_set_docker_token_` + uuidRegex + ` --rm --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock --entrypoint bash ubuntu -c mkdir -p ~/.docker/ && cat << EOF > ~/.docker/config.json
{
  "auths": {
    "https://asia.gcr.io": {
      "auth": "b2F1dGgyYWNjZXNzdG9rZW46RklSU1Q="
    },
    "https://b.gcr.io": {
      "auth": "b2F1dGgyYWNjZXNzdG9rZW46RklSU1Q="
    },
    "https://bucket.gcr.io": {
      "auth": "b2F1dGgyYWNjZXNzdG9rZW46RklSU1Q="
    },
    "https://eu.gcr.io": {
      "auth": "b2F1dGgyYWNjZXNzdG9rZW46RklSU1Q="
    },
    "https://gcr-staging.sandbox.google.com": {
      "auth": "b2F1dGgyYWNjZXNzdG9rZW46RklSU1Q="
    },
    "https://gcr.io": {
      "auth": "b2F1dGgyYWNjZXNzdG9rZW46RklSU1Q="
    },
    "https://us.gcr.io": {
      "auth": "b2F1dGgyYWNjZXNzdG9rZW46RklSU1Q="
    }
  }
}
EOF`,
		"docker run --name cloudbuild_update_docker_token_" + uuidRegex + " --rm --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock --entrypoint bash ubuntu -c sed -i 's/b2F1dGgyYWNjZXNzdG9rZW46RklSU1Q=/b2F1dGgyYWNjZXNzdG9rZW46U0VDT05E/g' ~/.docker/config.json",
	}, "\n")
	if match, _ := regexp.MatchString(want, got); !match {
		t.Errorf("Commands didn't match!\n===Want:\n%s\n===Got:\n%s", want, got)
	}
}

func TestWorkdir(t *testing.T) {
	for _, c := range []struct {
		rsDir, stepDir, want string
	}{
		{"", "", "/workspace"},
		{"", "relstep", "/workspace/relstep"},
		{"relrs", "relstep", "/workspace/relrs/relstep"},
		{"", "/absstep", "/absstep"},
		{"relrs", "/absstep", "/absstep"}, // repoSource.dir is ignored.
		{"..", "", "/"},
		{"..", "..", "/"},
		{"a", "../../", "/"},
		{"a/b/", "../../", "/workspace"},
		{"a/b/c", "../../", "/workspace/a"},
	} {
		got := workdir(c.rsDir, c.stepDir)
		if got != c.want {
			t.Errorf("workdir(%q, %q); got %q, want %q", c.rsDir, c.stepDir, got, c.want)
		}
	}
}

func TestBuildOutput(t *testing.T) {
	r := newMockRunner(t, "desc")
	r.dockerRunHandler = func([]string, io.Writer, io.Writer) error { return nil }

	for _, c := range []struct {
		contents    string // JSON file contents, if omitted no file is written.
		wantDigests []imageDigest
	}{{
		// Single image.
		contents:    `{"image":"image","digest":"sha256:deadbeef"}`,
		wantDigests: []imageDigest{{"image", "sha256:deadbeef"}},
	}, {
		// Multiple images.
		contents: `{"image":"image","digest":"sha256:deadbeef"}
{"image":"image2","digest":"sha256:faceb00kcafe"}`,
		wantDigests: []imageDigest{{"image", "sha256:deadbeef"}, {"image2", "sha256:faceb00kcafe"}},
	}, {
		// Invalid JSON, no outputs but no error.
		contents:    "utter garbage",
		wantDigests: nil,
	}, {
		// No file, no outputs but no error.
		contents:    "",
		wantDigests: nil,
	}, {
		// One valid image, then invalid JSON.
		contents: `{"image":"image","digest":"sha256:deadbeef"}
utter garbage`,
		wantDigests: []imageDigest{{"image", "sha256:deadbeef"}},
	}, {
		// Invalid JSON stops reading the file.
		contents: `utter garbage
{"image":"image","digest":"sha256:deadbeef"}`,
		wantDigests: nil,
	}} {
		fs := afero.NewMemMapFs()
		b := New(r, commonBuildRequest, mockTokenSource(), nopBuildLogger{}, nopEventLogger{}, "", fs, true, false, false)
		b.enableStepOutputs = true

		if c.contents != "" {
			tmp := afero.GetTempDir(fs, "")
			if err := b.fs.MkdirAll(tmp, os.FileMode(os.O_CREATE)); err != nil {
				t.Errorf("MkDirAll(%q): %v", afero.GetTempDir(fs, ""), err)
			}
			p := path.Join(tmp, dockerImageOutputFile)
			if err := afero.WriteFile(fs, p, []byte(c.contents), os.FileMode(os.O_CREATE)); err != nil {
				t.Fatalf("WriteFile(%q -> %q): %v", c.contents, p, err)
			}
		}

		if err := b.runStep(context.Background(), 0); err != nil {
			t.Errorf("runStep failed: %v", err)
		}

		if !reflect.DeepEqual(b.imageDigests, c.wantDigests) {
			t.Errorf("Collected image digests %v, want %v", b.imageDigests, c.wantDigests)
		}
	}
}

type nopBuildLogger struct{}

func (nopBuildLogger) WriteMainEntry(string)                  { return }
func (nopBuildLogger) Close() error                           { return nil }
func (nopBuildLogger) MakeWriter(string, int, bool) io.Writer { return ioutil.Discard }

type nopEventLogger struct{}

func (nopEventLogger) StartStep(context.Context, int, time.Time) error        { return nil }
func (nopEventLogger) FinishStep(context.Context, int, bool, time.Time) error { return nil }

// Test coverage to confirm that we don't have the data race fixed in cl/190142977.
func TestDataRace(t *testing.T) {
	build := pb.Build{
		Steps:  []*pb.BuildStep{{Name: "gcr.io/step-zero", Args: []string{"sleep", "1"}}},
		Images: []string{"one", "two", "three", "four", "five"},
	}
	r := newMockRunner(t, "race")
	r.dockerRunHandler = func([]string, io.Writer, io.Writer) error { return nil }
	b := New(r, build, mockTokenSource(), nopBuildLogger{}, nopEventLogger{}, "", afero.NewMemMapFs(), true, false, false)
	ctx := context.Background()

	go b.Start(ctx) // updates b.Timing.BuildTotal

	// Set up goroutines to tickle potential race conditions...
	starter := make(chan struct{})
	go func() {
		<-starter
		b.pushImages(ctx)
	}()
	go func() {
		<-starter
		// Logging b.Summary() causes its return value to be read, provoking a race
		// with it being written by b.pushImages().
		t.Logf("Summary is %v", b.Summary())
	}()

	close(starter)
	<-b.Done // Wait for build to complete.
}
