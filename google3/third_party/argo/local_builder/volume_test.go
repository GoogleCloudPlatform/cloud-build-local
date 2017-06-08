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

package volume

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
)

const (
	volumeName = "gcb-local-vol"
)

type mockRunner struct {
	mu           sync.Mutex
	t            *testing.T
	testCaseName string
	commands     []string
}

func newMockRunner(t *testing.T) *mockRunner {
	return &mockRunner{
		t: t,
	}
}

func (r *mockRunner) Run(args []string, in io.Reader, out, err io.Writer, _ string) error {
	r.mu.Lock()
	r.commands = append(r.commands, strings.Join(args, " "))
	r.mu.Unlock()
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

func TestSetup(t *testing.T) {
	r := newMockRunner(t)
	vol := New(volumeName, r)

	if err := vol.Setup(); err != nil {
		t.Errorf("Setup failed: %v", err)
	}

	got := strings.Join(r.commands, "\n")
	want := strings.Join([]string{`docker volume create --name gcb-local-vol
docker run -v gcb-local-vol:/workspace --name gcb-local-vol-helper busybox`}, "\n")

	if got != want {
		t.Errorf("Commands didn't match!\n===Want:\n%s\n===Got:\n%s", want, got)
	}
}

func TestCopy(t *testing.T) {
	r := newMockRunner(t)
	vol := New(volumeName, r)

	dir := "/wherever/you/want"
	if err := vol.Copy(dir); err != nil {
		t.Errorf("Copy failed: %v", err)
	}

	got := strings.Join(r.commands, "\n")
	want := strings.Join([]string{fmt.Sprintf(`docker cp %s gcb-local-vol-helper:/workspace`, dir)}, "\n")

	if got != want {
		t.Errorf("Commands didn't match!\n===Want:\n%s\n===Got:\n%s", want, got)
	}
}

func TestClose(t *testing.T) {
	r := newMockRunner(t)
	vol := New(volumeName, r)

	if err := vol.Close(); err != nil {
		t.Errorf("Close failed: %v", err)
	}

	got := strings.Join(r.commands, "\n")
	want := strings.Join([]string{fmt.Sprintf(`docker rm gcb-local-vol-helper
docker volume rm %s`, volumeName)}, "\n")

	if got != want {
		t.Errorf("Commands didn't match!\n===Want:\n%s\n===Got:\n%s", want, got)
	}
}
