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

package gsutil

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
)

const (
	// errInvalidURL is the error string returned when a GCS URL is not prefixed with 'gs://'.
	errInvalidURL = "CommandException: \"ls\" command does not support \"file://\" URLs. Did you mean to use a gs:// URL?"
	// errBucketNotFound is the error string returned when a GCS bucket does not exist.
	errBucketNotFound = "BucketNotFoundException: 404 bucket does not exist."
)

type mockRunner struct {
	
	t            *testing.T
	testCaseName string
	commands     []string
	buckets      []string
	objects      map[string][]string // maps GCS bucket names to a list of GCS objects
}

func newMockRunner(t *testing.T, testCaseName string) *mockRunner {
	return &mockRunner{
		t:            t,
		testCaseName: testCaseName,
		// Default buckets.
		buckets: []string{
			"gs://bucket-one",
			"gs://bucket-two",
			"gs://bucket-three",
		},
		// Default objects.
		objects: map[string][]string{
			"gs://bucket-one":   {"hall.json"},
			"gs://bucket-two":   {"maraudersmap.jpg", "passwords.txt"},
			"gs://bucket-three": {"bell.jar", "google.doc", "html.html"},
		},
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
	r.commands = append(r.commands, fmt.Sprintf("MkdirAll(%s)", dir))
	return nil
}

func (r *mockRunner) WriteFile(path, contents string) error {
	r.commands = append(r.commands, fmt.Sprintf("WriteFile(%s,%q)", path, contents))
	return nil
}

func (r *mockRunner) Clean() error {
	return nil
}

func (r *mockRunner) Run(args []string, in io.Reader, out, err io.Writer, _ string) error {
	r.commands = append(r.commands, strings.Join(args, " "))

	if startsWith(args, "docker", "run") && contains(args, "gcr.io/cloud-builders/gsutil", "ls") {
		// Simulate 'gsutil ls' command (https://cloud.google.com/storage/docs/gsutil/commands/ls).
		lastArg := args[len(args)-1]

		if lastArg == "ls" {
			// List all the buckets
			for i, b := range r.buckets {
				io.WriteString(out, b)
				// Do not print new line after last bucket.
				if i < len(r.buckets)-1 {
					io.WriteString(out, "\n")
				}
			}
			return nil
		}

		// The last arg is bucket URL. There is no need to support gsutil in the mockRunner.
		// Check that the URL is prefixed with 'gs://'.
		bucket := lastArg
		if !strings.HasPrefix(bucket, "gs://") {
			return errors.New(errInvalidURL)
		}
		// Check that the bucket exists.
		found := false
		for _, b := range r.buckets {
			if bucket == b {
				found = true
			}
		}
		if !found {
			return errors.New(errBucketNotFound)
		}
		// List bucket contents.
		for i, obj := range r.objects[bucket] {
			io.WriteString(out, obj)
			// Do not print new line after last object.
			if i < len(r.objects[bucket])-1 {
				io.WriteString(out, "\n")
			}
		}
	}
	return nil
}

func TestCheckBucket(t *testing.T) {
	newUUID = func() string { return "someuuid" }
	defer func() { newUUID = uuid.New }()

	testCases := []struct {
		name       string
		bucket     string
		wantExists bool
	}{{
		name:       "Exists",
		bucket:     "gs://bucket-one",
		wantExists: true,
	}, {
		name:   "DoesNotExist",
		bucket: "gs://i-do-not-exist",
	}, {
		name:   "InvalidName",
		bucket: "i-am-not-a-gcs-url",
	}}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := newMockRunner(t, tc.name)

			gsutilHelper := New(r)
			exists := gsutilHelper.BucketExists(tc.bucket)

			if tc.wantExists != exists {
				t.Errorf("got gsutilHelper.CheckBucket(%s) = %v, want %v", tc.bucket, exists, tc.wantExists)
			}

			// Check docker run commands and arguments.
			wantCommands := []string{
				"docker run --name cloudbuild_gsutil_ls_" + newUUID() +
					" --rm --volume /var/run/docker.sock:/var/run/docker.sock --network cloudbuild gcr.io/cloud-builders/gsutil ls " + tc.bucket,
			}

			if len(r.commands) != len(wantCommands) {
				t.Errorf("%s: Wrong number of commands: want %d, got %d", tc.name, len(wantCommands), len(r.commands))
			}
			for i := range r.commands {
				if match, _ := regexp.MatchString(wantCommands[i], r.commands[i]); !match {
					t.Errorf("%s: command %d didn't match!\n===Want:\n%s\n===Got:\n%s", tc.name, i, wantCommands[i], r.commands[i])
				}
			}
			got := strings.Join(r.commands, "\n")
			want := strings.Join(wantCommands, "\n")
			if match, _ := regexp.MatchString(want, got); !match {
				t.Errorf("%s: Commands didn't match!\n===Want:\n%s\n===Got:\n%s", tc.name, want, got)
			}
		})
	}
}
