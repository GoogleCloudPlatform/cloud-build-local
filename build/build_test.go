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
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/container-builder-local/buildlog"
	"github.com/GoogleCloudPlatform/container-builder-local/runner"
	"golang.org/x/oauth2"

	cb "google.golang.org/genproto/googleapis/devtools/cloudbuild/v1"
)

type mockRunner struct {
	mu               sync.Mutex
	t                *testing.T
	testCaseName     string
	commands         []string
	localImages      map[string]bool
	remoteImages     map[string]bool
	remotePushesFail bool
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

func (r *mockRunner) Run(args []string, in io.Reader, out, err io.Writer, _ string) error {
	r.mu.Lock()
	r.commands = append(r.commands, strings.Join(args, " "))
	r.mu.Unlock()
	if startsWith(args, "docker", "inspect") {
		tag := args[2]
		if r.localImages[tag] {
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
		return fmt.Errorf("exit status 1")
	}
	if startsWith(args, "docker", "run") && contains(args, "gcr.io/cloud-builders/docker", "pull") {
		tag := args[len(args)-1]
		if r.remoteImages[tag] {
			// Successful pull.
			io.WriteString(out, `Using default tag: latest
latest: Pulling from test-argo/busybox
a5d4c53980c6: Pull complete
b41c5284db84: Pull complete
Digest: sha256:digestRemote
Status: Downloaded newer image for gcr.io/test-argo/busybox:latest
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
			return fmt.Errorf("exit status 1")
		}
		tag := args[len(args)-1]
		io.WriteString(out, `The push refers to a repository [skelterjohn/ubuntu] (len: 1)
ca4d7b1b9a51: Image already exists
a467a7c6794f: Image successfully pushed
ea358092da77: Image successfully pushed
2332d8973c93: Image successfully pushed
`)
		if tag != "gcr.io/build-output-tag-no-digest" {
			io.WriteString(out, "latest: digest: sha256:deadb33f000deadb33f0000123456789abcdeffffffffffffff size: 9914\n")
		}
		// Successful push.
		return nil
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
	if startsWith(args, "gsutil", "cp") {
		uri := args[2]
		filename := args[3]
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
	if startsWith(args, "gsutil", "cat") {
		uri := args[3]
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
	if startsWith(args, "docker", "rm") {
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

var commonBuildRequest = cb.Build{
	Source: &cb.Source{
		Source: &cb.Source_RepoSource{
			RepoSource: &cb.RepoSource{},
		},
	},
	SourceProvenance: &cb.SourceProvenance{
		ResolvedRepoSource: &cb.RepoSource{
			Revision: &cb.RepoSource_CommitSha{CommitSha: "commit-sha"},
		},
	},
	Steps: []*cb.BuildStep{{
		Name: "gcr.io/my-project/my-compiler",
	}, {
		Name: "gcr.io/my-project/my-builder",
		Env:  []string{"FOO=bar", "BAZ=buz"},
		Args: []string{"a", "b", "c"},
		Dir:  "foo/bar/../baz",
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
	testCases := []struct {
		name         string
		buildRequest cb.Build
		wantErr      error
		wantCommands []string
	}{{
		name:         "TestFetchBuilder",
		buildRequest: commonBuildRequest,
		wantCommands: []string{
			"docker inspect gcr.io/my-project/my-compiler",
			"docker run --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock gcr.io/cloud-builders/docker pull gcr.io/my-project/my-compiler",
			"docker inspect gcr.io/my-project/my-builder",
			"docker run --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock gcr.io/cloud-builders/docker pull gcr.io/my-project/my-builder",
		},
	}, {
		name: "TestFetchBuilderExists",
		buildRequest: cb.Build{
			Steps: []*cb.BuildStep{{
				Name: "gcr.io/cached-build-step",
			}},
		},
		wantCommands: []string{
			"docker inspect gcr.io/cached-build-step",
		},
	}, {
		name: "TestFetchBuilderFail",
		buildRequest: cb.Build{
			Steps: []*cb.BuildStep{{
				Name: "gcr.io/invalid-build-step",
			}},
		},
		// no image in remoteImages
		wantCommands: []string{
			"docker inspect gcr.io/invalid-build-step",
			"docker run --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock gcr.io/cloud-builders/docker pull gcr.io/invalid-build-step",
		},
		wantErr: errors.New(`error pulling build step "gcr.io/invalid-build-step": exit status 1 for tag "gcr.io/invalid-build-step"`),
	}}
	for _, tc := range testCases {
		r := newMockRunner(t, tc.name)
		b := New(r, tc.buildRequest, mockTokenSource(), &buildlog.BuildLog{}, "", true, false)
		var gotErr error
		var gotDigest string
		wantDigest := ""
		for i, bs := range tc.buildRequest.Steps {
			gotDigest, gotErr = b.fetchBuilder(bs.Name, fmt.Sprintf("Step #%d", i))
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
			t.Errorf("%s: Wanted error %v, but got %v", tc.name, tc.wantErr, gotErr)
		}
		if tc.wantCommands != nil {
			got := strings.Join(r.commands, "\n")
			want := strings.Join(tc.wantCommands, "\n")
			if got != want {
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
			Request: cb.Build{
				Steps: []*cb.BuildStep{{
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
			Request: cb.Build{
				Steps: []*cb.BuildStep{{
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
		name: "IDWithMultipleDepenedencies",
		build: Build{
			Request: cb.Build{
				Steps: []*cb.BuildStep{{
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
			Request: cb.Build{
				Steps: []*cb.BuildStep{{
					Name:    "gcr.io/cached-build-step",
					WaitFor: []string{StartStep},
				}},
			},
		},
	}, {
		name: "StartStepsMultipleDependencies",
		build: Build{
			Request: cb.Build{
				Steps: []*cb.BuildStep{{
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
			Request: cb.Build{
				Steps: []*cb.BuildStep{{
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
			Request: cb.Build{
				Steps: []*cb.BuildStep{{
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
	testCases := []struct {
		name                 string
		buildRequest         cb.Build
		opFailsToWrite       bool
		opFailsWithErr       bool
		argsOfStepAfterError string
		wantErr              error
		wantCommands         []string
	}{{
		name:         "TestRunBuilder",
		buildRequest: commonBuildRequest,
		wantCommands: []string{
			"docker volume create --name homevol",
			"docker inspect gcr.io/my-project/my-compiler",
			"docker run --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock gcr.io/cloud-builders/docker pull gcr.io/my-project/my-compiler",
			dockerRunString(0) + " gcr.io/my-project/my-compiler",
			"docker inspect gcr.io/my-project/my-builder",
			"docker run --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock gcr.io/cloud-builders/docker pull gcr.io/my-project/my-builder",
			dockerRunInStepDir(1, "foo/baz") +
				" --env FOO=bar" +
				" --env BAZ=buz" +
				" gcr.io/my-project/my-builder a b c",
			"docker inspect gcr.io/build-output-tag-1",
			"docker inspect gcr.io/build-output-tag-2",
			"docker inspect gcr.io/build-output-tag-no-digest",
			"docker rm -f step_0 step_1",
			"docker volume rm homevol",
		},
	}, {
		name:           "TestRunBuilderFailExplicit",
		buildRequest:   commonBuildRequest,
		opFailsWithErr: true,
		wantErr:        errors.New(`build step "gcr.io/my-project/my-compiler" failed: exit status 1`),
	}, {
		name:           "TestRunBuilderFailImplicit",
		buildRequest:   commonBuildRequest,
		opFailsToWrite: true,
		wantErr:        errors.New(`failed to find one or more images after execution of build steps: ["gcr.io/build-output-tag-1" "gcr.io/build-output-tag-no-digest"]`),
	}, {
		name:           "TestRunBuilderFailConfirmNoContinuation",
		buildRequest:   commonBuildRequest,
		opFailsWithErr: true,
		wantErr:        errors.New(`build step "gcr.io/my-project/my-compiler" failed: exit status 1`),
	}}
	for _, tc := range testCases {
		r := newMockRunner(t, tc.name)
		r.dockerRunHandler = func(_ []string, _, _ io.Writer) error {
			if !tc.opFailsToWrite {
				r.localImages["gcr.io/build-output-tag-1"] = true
				r.localImages["gcr.io/build-output-tag-no-digest"] = true
			}
			r.localImages["gcr.io/build-output-tag-2"] = true
			if tc.opFailsWithErr {
				return fmt.Errorf("exit status 1")
			}
			return nil
		}
		b := New(r, tc.buildRequest, mockTokenSource(), &buildlog.BuildLog{}, "", true, false)
		gotErr := b.runBuildSteps()
		if !reflect.DeepEqual(gotErr, tc.wantErr) {
			t.Errorf("%s: Wanted error %q, but got %q", tc.name, tc.wantErr, gotErr)
		}
		if tc.wantCommands != nil {
			got := strings.Join(r.commands, "\n")
			want := strings.Join(tc.wantCommands, "\n")
			if got != want {
				t.Errorf("%s: Commands didn't match!\n===Want:\n%s\n===Got:\n%s", tc.name, want, got)
			}
		}
	}
}

func TestBuildStepOrder(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name         string
		buildRequest cb.Build
		wantErr      error
		wantCommands []string
	}{{
		name: "TestRunBuilderSequentialOneStep",
		buildRequest: cb.Build{
			Steps: []*cb.BuildStep{{
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
		buildRequest: cb.Build{
			Steps: []*cb.BuildStep{{
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
		buildRequest: cb.Build{
			Steps: []*cb.BuildStep{{
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
		buildRequest: cb.Build{
			Steps: []*cb.BuildStep{{
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
		buildRequest: cb.Build{
			Steps: []*cb.BuildStep{{
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
		buildRequest: cb.Build{
			Steps: []*cb.BuildStep{{
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
		b := New(r, tc.buildRequest, mockTokenSource(), &buildlog.BuildLog{}, "", true, false)
		errorFromFunction := make(chan error)
		go func() {
			errorFromFunction <- b.runBuildSteps()
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
	testCases := []struct {
		name             string
		buildRequest     cb.Build
		wantErr          error
		wantCommands     []string
		remotePushesFail bool
	}{{
		name:             "TestPushImages",
		buildRequest:     commonBuildRequest,
		remotePushesFail: false,
		wantCommands: []string{
			"docker run --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock gcr.io/cloud-builders/docker push gcr.io/build-output-tag-1",
			"docker run --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock gcr.io/cloud-builders/docker push gcr.io/build-output-tag-2",
			"docker run --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock gcr.io/cloud-builders/docker push gcr.io/build-output-tag-no-digest",
		},
	}, {
		name:             "TestPushImagesFail",
		buildRequest:     commonBuildRequest,
		remotePushesFail: true,
		wantErr:          errors.New(`error pushing image "gcr.io/build-output-tag-1": exit status 1`),
	}}
	for _, tc := range testCases {
		r := newMockRunner(t, tc.name)
		b := New(r, tc.buildRequest, mockTokenSource(), &buildlog.BuildLog{}, "", true, true)
		r.remotePushesFail = tc.remotePushesFail
		gotErr := b.pushImages()
		if !reflect.DeepEqual(gotErr, tc.wantErr) {
			t.Errorf("%s: Wanted error %q, but got %q", tc.name, tc.wantErr, gotErr)
		}
		if tc.wantCommands != nil {
			got := strings.Join(r.commands, "\n")
			want := strings.Join(tc.wantCommands, "\n")
			if got != want {
				t.Errorf("%s: Commands didn't match!\n===Want:\n%s\n===Got:\n%s", tc.name, want, got)
			}
			// Validate side-effects of pushing docker images.
			wantDigest := "sha256:deadb33f000deadb33f0000123456789abcdeffffffffffffff"
			if got := b.imageDigests["gcr.io/build-output-tag-1"]; got != wantDigest {
				t.Errorf("%s: Got digest %q for gcr.io/build-output-tag-1. Wanted %q", tc.name, got, wantDigest)
			}
			if got := b.imageDigests["gcr.io/build-output-tag-2"]; got != wantDigest {
				t.Errorf("%s: Got digest %q for gcr.io/build-output-tag-2. Wanted %q", tc.name, got, wantDigest)
			}
			if got := b.imageDigests["gcr.io/build-output-tag-no-digest"]; got != "" {
				t.Errorf("%s: Got digest %q for gcr.io/build-output-tag-no-digest. Wanted %q", tc.name, got, "")
			}
			summary := b.Summary()
			wantSummary := BuildSummary{
				// We didn't use the Start method, so the build status never got updated.
				Status: "",
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
				},
				// gcr.io/build-output-tag-no-digest doesn't show up at all b/c it has no digest!
				},
				BuildStepImages: []string{"", ""},
			}
			if !reflect.DeepEqual(summary, wantSummary) {
				t.Errorf("unexpected build summary:\n got %+v\nwant %+v", summary, wantSummary)
			}
		}
	}

}

func TestBuildStepConcurrency(t *testing.T) {
	t.Parallel()
	req := cb.Build{
		// This build is set up such that if build steps don't run concurrently,
		// the build will time out. Specifically, if A were to run first and alone,
		// it would timeout waiting for B to run.  If B were to run first and
		// alone, it would timeout waiting for A to run. Thus A and B must run
		// concurrently for this build to not time out.
		//
		// See comments on type fakeRunner (below) which document how the steps
		// signal each other.
		Steps: []*cb.BuildStep{{
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
	b := New(r, req, mockTokenSource(), &buildlog.BuildLog{}, "", true, false)
	ret := make(chan error)
	go func() {
		ret <- b.runBuildSteps()
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
func (f *fakeRunner) Run(args []string, _ io.Reader, _, _ io.Writer, _ string) error {
	// The "+1" is for the name of the container which is appended to the
	// dockerRunArgs base command.
	b := New(nil, cb.Build{}, nil, &buildlog.BuildLog{}, "", true, false)
	argCount := len(b.dockerRunArgs("", 0)) + 1
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
	b := New(nil, cb.Build{}, nil, &buildlog.BuildLog{}, "", true, false)
	dockerRunArg := b.dockerRunArgs(stepDir, idx)
	return strings.Join(dockerRunArg, " ")
}

func (f *fakeRunner) MkdirAll(_ string) error {
	return nil
}

func (f *fakeRunner) WriteFile(_, _ string) error {
	return nil
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
	b := New(nil, cb.Build{}, nil, &buildlog.BuildLog{}, "", true, false)
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
	testCases := []struct {
		name         string
		buildRequest cb.Build
		wantErr      error
		wantCommands []string
	}{{
		name: "TestWithoutEntrypoint",
		buildRequest: cb.Build{
			Steps: []*cb.BuildStep{{
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
		buildRequest: cb.Build{
			Steps: []*cb.BuildStep{{
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
		b := New(r, tc.buildRequest, mockTokenSource(), &buildlog.BuildLog{}, "", true, false)
		errorFromFunction := make(chan error)
		go func() {
			errorFromFunction <- b.runBuildSteps()
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

func TestPushDigestScraping(t *testing.T) {
	cases := []struct {
		desc     string
		output   string
		expected map[string]string
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
		expected: map[string]string{
			"1.12.6": "sha256:22c754d23e8461f6992e900290f4a146fda90eaf89b0cbdbdfd3aaa503dad4f6",
			"1.9.1":  "sha256:16c183bac00b282e420fbc6d3f3bf56f9ae18d85c36c4f8850c951ea49ca5dc1",
			"latest": "sha256:22c754d23e8461f6992e900290f4a146fda90eaf89b0cbdbdfd3aaa503dad4f6",
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
		for i, d := range c.expected {
			if gd, ok := got[i]; !ok || gd != d {
				t.Errorf("%s: did not get expected %s:%s", c.desc, i, d)
			}
		}
	}
}

func TestDigestResolution(t *testing.T) {
	var b *Build

	cases := []struct {
		desc     string
		image    string
		digests  map[string]string
		expected map[string]string
	}{{
		desc:  "listed untagged, pushed latest",
		image: "foo",
		digests: map[string]string{
			"latest": "123",
		},
		expected: map[string]string{
			"foo":        "123",
			"foo:latest": "123",
		},
	}, {
		desc:  "listed latest, pushed latest",
		image: "foo:latest",
		digests: map[string]string{
			"latest": "123",
		},
		expected: map[string]string{
			"foo":        "123",
			"foo:latest": "123",
		},
	}, {
		desc:  "listed untagged, pushed x and y",
		image: "foo",
		digests: map[string]string{
			"x": "123",
			"y": "456",
		},
		expected: map[string]string{
			"foo:x": "123",
			"foo:y": "456",
		},
	}, {
		desc:  "listed x, pushed x",
		image: "foo:x",
		digests: map[string]string{
			"x": "123",
		},
		expected: map[string]string{
			"foo:x": "123",
		},
	}}
	for _, c := range cases {
		got := b.resolveDigestsForImage(c.image, c.digests)
		if len(got) != len(c.expected) {
			t.Errorf("%s: wrong number of results; want %d, got %d", c.desc, len(c.expected), len(got))
		}
		for i, d := range c.expected {
			if gd, ok := got[i]; !ok || gd != d {
				t.Errorf("%s: did not get expected %s:%s", c.desc, i, d)
			}
		}
	}
}

func TestStart(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		push         bool
		buildRequest cb.Build
		wantCommands []string
	}{{
		name: "Build and push",
		push: true,
		buildRequest: cb.Build{
			Steps: []*cb.BuildStep{{
				Name: "gcr.io/my-project/my-builder",
				Args: []string{"a"},
			}},
			Images: []string{"gcr.io/build"},
		},
		wantCommands: []string{
			"docker volume create --name homevol",
			"docker inspect gcr.io/my-project/my-builder",
			"docker run --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock gcr.io/cloud-builders/docker pull gcr.io/my-project/my-builder",
			dockerRunString(0) + " gcr.io/my-project/my-builder a",
			"docker inspect gcr.io/build",
			"docker rm -f step_0",
			"docker volume rm homevol",
			"docker run --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock gcr.io/cloud-builders/docker push gcr.io/build",
		},
	}, {
		name: "Build without pushing images",
		push: false,
		buildRequest: cb.Build{
			Steps: []*cb.BuildStep{{
				Name: "gcr.io/my-project/my-builder",
				Args: []string{"a"},
			}},
			Images: []string{"gcr.io/build"},
		},
		wantCommands: []string{
			"docker volume create --name homevol",
			"docker inspect gcr.io/my-project/my-builder",
			"docker run --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock gcr.io/cloud-builders/docker pull gcr.io/my-project/my-builder",
			dockerRunString(0) + " gcr.io/my-project/my-builder a",
			"docker inspect gcr.io/build",
			"docker rm -f step_0",
			"docker volume rm homevol",
		},
	}} {
		r := newMockRunner(t, tc.name)
		r.dockerRunHandler = func(args []string, _, _ io.Writer) error {
			r.localImages["gcr.io/build"] = true
			return nil
		}
		b := New(r, tc.buildRequest, mockTokenSource(), &buildlog.BuildLog{}, "", true, tc.push)
		b.Start()
		<-b.Done

		if got, want := b.GetStatus(), StatusDone; got != want {
			t.Errorf("Unexpected status. got %q, want %q", got, want)
		}

		got := strings.Join(r.commands, "\n")
		want := strings.Join(tc.wantCommands, "\n")
		if got != want {
			t.Errorf("%s: Commands didn't match!\n===Want:\n%s\n===Got:\n%s", tc.name, want, got)
		}
	}
}

// TestUpdateDockerAccessToken tests the commands executed when setting and
// updating Docker access tokens.
func TestUpdateDockerAccessToken(t *testing.T) {
	t.Parallel()
	r := newMockRunner(t, "TestUpdateDockerAccessToken")
	r.dockerRunHandler = func(args []string, _, _ io.Writer) error { return nil }
	b := New(r, cb.Build{}, nil, nil, "", false, false)

	// If UpdateDockerAccessToken is called before SetDockerAccessToken, we
	// should get an error.
	if err := b.UpdateDockerAccessToken("INVALID"); err == nil {
		t.Errorf("Expected error when calling UpdateDockerAccessToken first, got none: %v", err)
	}

	if err := b.SetDockerAccessToken("FIRST"); err != nil {
		t.Errorf("SetDockerAccessToken: %v", err)
	}
	if got, want := b.prevGCRAuth, base64.StdEncoding.EncodeToString([]byte("oauth2accesstoken:FIRST")); got != want {
		t.Errorf("After SetDockerAccessToken, GCR auth is %q, want %q", got, want)
	}

	if err := b.UpdateDockerAccessToken("SECOND"); err != nil {
		t.Errorf("UpdateDockerAccessToken: %v", err)
	}
	if got, want := b.prevGCRAuth, base64.StdEncoding.EncodeToString([]byte("oauth2accesstoken:SECOND")); got != want {
		t.Errorf("After SetDockerAccessToken, GCR auth is %q, want %q", got, want)
	}

	got := strings.Join(r.commands, "\n")
	want := strings.Join([]string{
		`docker run --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock --entrypoint bash ubuntu -c mkdir -p ~/.docker/ && cat << EOF > ~/.docker/config.json
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
		"docker run --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock --entrypoint bash ubuntu -c sed -i 's/b2F1dGgyYWNjZXNzdG9rZW46RklSU1Q=/b2F1dGgyYWNjZXNzdG9rZW46U0VDT05E/g' ~/.docker/config.json",
	}, "\n")
	if got != want {
		t.Errorf("Commands didn't match!\n===Want:\n%s\n===Got:\n%s", want, got)
	}
}
