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

// Package smoke_test runs an end-to-end tests.
// A local builder binary is required to run these tests against.
// There are some prerequisites to run these tests:
// - docker
// - gcloud
// - any other tool called in the builds
package smoke_test

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var (
	projectId  = flag.String("project_id", "argo-local-builder", "ProjectId to use in the tests (in case the test needs other GCP services)")
	binaryPath = flag.String("binary_path", "container-builder-local", "Path to the local builder binary")
	filesPath  = flag.String("files_path", ".", "Path to the config files")
)

func TestFlags(t *testing.T) {
	testCases := []struct {
		desc       string
		flags      []string
		wantErr    bool
		wantStdout string
	}{{
		desc:       "version flag",
		flags:      []string{"--version"},
		wantStdout: "Version:",
	}, {
		desc:       "help flag",
		flags:      []string{"--help"},
		wantStdout: "substitutions",
	}, {
		desc:    "no source",
		flags:   []string{},
		wantErr: true,
	}, {
		desc:    "flags after source",
		flags:   []string{*filesPath, "--config", filepath.Join(*filesPath, "cloudbuild.yaml")},
		wantErr: true,
	}, {
		desc:       "unexisting config file",
		flags:      []string{"--config", filepath.Join(*filesPath, "donotexist.yaml"), *filesPath},
		wantStdout: "Unable to read config file",
	}, {
		desc:       "happy dryrun case",
		flags:      []string{"--config", filepath.Join(*filesPath, "cloudbuild.yaml"), *filesPath},
		wantStdout: "DONE",
		// }, {
		// 	desc:       "happy case",
		// 	flags:      []string{"--config", filepath.Join(*filesPath, "cloudbuild.yaml"), "--dryrun=false", *filesPath},
		// 	wantStdout: "DONE",
	}}

	for _, tc := range testCases {
		cmd := exec.Command(*binaryPath, tc.flags...)
		stdout, err := cmd.CombinedOutput()
		// fmt.Println("--> ", cmd, err, string(stdout))
		if err != nil && !tc.wantErr {
			t.Errorf("%s: Command returned an error: %v", tc.desc, err)
		}
		if err == nil && tc.wantErr {
			t.Errorf("%s: Command did not return the expected error", tc.desc)
		}
		if !strings.Contains(string(stdout), tc.wantStdout) {
			t.Errorf("%s: Wrong stdout message; got %q, want %q", tc.desc, string(stdout), tc.wantStdout)
		}
	}
}

func TestConfigs(t *testing.T) {
	fmt.Println("we will go through all the cloudbuild.yaml files in this directory")

	if err := filepath.Walk(*filesPath, func(path string, f os.FileInfo, err error) error {
		if filepath.Ext(path) == ".yaml" {
			flags := []string{"--config", path, "--dryrun=false", *filesPath}
			cmd := exec.Command(*binaryPath, flags...)
			stdout, err := cmd.CombinedOutput()
			fmt.Println("--> ", cmd, err, string(stdout))
			if !strings.Contains(string(stdout), "DONE") {
				t.Errorf("%s: Build failed: %s", path, string(stdout))
			}
		}
		return nil
	}); err != nil {
		t.Errorf("Error testing configs: %v", err)
	}
}
