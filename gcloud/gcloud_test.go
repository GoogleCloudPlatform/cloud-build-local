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

package gcloud

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
)

type mockRunner struct {
	mu           sync.Mutex
	t            *testing.T
	testCaseName string
	commands     []string
	projectID    string
}

func newMockRunner(t *testing.T, projectID string) *mockRunner {
	return &mockRunner{
		t:         t,
		projectID: projectID,
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

func (r *mockRunner) Run(args []string, in io.Reader, out, err io.Writer, _ string) error {
	r.mu.Lock()
	r.commands = append(r.commands, strings.Join(args, " "))
	r.mu.Unlock()

	if startsWith(args, "gcloud", "config", "list") {
		fmt.Fprintln(out, r.projectID)
	} else if startsWith(args, "gcloud", "projects", "describe") {
		fmt.Fprintln(out, "1234")
	} else if startsWith(args, "gcloud", "config", "config-helper", "--format=value(credential.access_token)") {
		fmt.Fprintln(out, "my-token")
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

func TestAccessToken(t *testing.T) {
	r := newMockRunner(t, "")
	token, err := AccessToken(r)
	if err != nil {
		t.Errorf("AccessToken failed: %v", err)
	}
	if token != "my-token" {
		t.Errorf("AccessToken failed returning the token; got %s, want %s", token, "my-token")
	}
	got := strings.Join(r.commands, "\n")
	want := "gcloud config config-helper --format=value(credential.access_token)"
	if got != want {
		t.Errorf("Commands didn't match!\n===Want:\n%s\n===Got:\n%s", want, got)
	}
}

func TestProjectInfo(t *testing.T) {
	r := newMockRunner(t, "my-project-id")
	projectInfo, err := ProjectInfo(r)
	if err != nil {
		t.Errorf("ProjectInfo failed: %v", err)
	}
	if projectInfo.ProjectID != "my-project-id" {
		t.Errorf("ProjectInfo failed returning the projectID; got %s, want %s", projectInfo.ProjectID, "my-project-id")
	}
	if projectInfo.ProjectNum != 1234 {
		t.Errorf("ProjectInfo failed returning the projectNum; got %d, want %d", projectInfo.ProjectNum, 1234)
	}
	got := strings.Join(r.commands, "\n")
	want := strings.Join([]string{`gcloud config list --format value(core.project)`,
		`gcloud projects describe my-project-id --format value(projectNumber)`}, "\n")
	if got != want {
		t.Errorf("Commands didn't match!\n===Want:\n%s\n===Got:\n%s", want, got)
	}
}

func TestProjectInfoError(t *testing.T) {
	r := newMockRunner(t, "")
	_, err := ProjectInfo(r)
	if err == nil {
		t.Errorf("ProjectInfo should fail when no projectId set in gcloud")
	}
}
