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
	"context"
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

func (r *mockRunner) Run(ctx context.Context, args []string, in io.Reader, out, err io.Writer) error {
	r.mu.Lock()
	r.commands = append(r.commands, strings.Join(args, " "))
	r.mu.Unlock()
	return nil
}

func TestSetup(t *testing.T) {
	ctx := context.Background()
	r := newMockRunner(t)
	vol := New(volumeName, r)

	if err := vol.Setup(ctx); err != nil {
		t.Errorf("Setup failed: %v", err)
	}

	got := strings.Join(r.commands, "\n")
	want := "docker volume create --name gcb-local-vol"
	if got != want {
		t.Errorf("Commands didn't match!\n===Want:\n%s\n===Got:\n%s", want, got)
	}
}

func TestCopy(t *testing.T) {
	ctx := context.Background()
	r := newMockRunner(t)
	vol := New(volumeName, r)

	if err := vol.Copy(ctx, "/wherever/you/want"); err != nil {
		t.Errorf("Copy failed: %v", err)
	}

	got := strings.Join(r.commands, "\n")
	want := `docker run -v gcb-local-vol:/workspace --name gcb-local-vol-helper busybox
docker cp /wherever/you/want gcb-local-vol-helper:/workspace`
	if got != want {
		t.Errorf("Commands didn't match!\n===Want:\n%s\n===Got:\n%s", want, got)
	}
}

func TestExport(t *testing.T) {
	ctx := context.Background()
	r := newMockRunner(t)
	vol := New(volumeName, r)

	if err := vol.Export(ctx, "/wherever/you/want"); err != nil {
		t.Errorf("Copy failed: %v", err)
	}

	got := strings.Join(r.commands, "\n")
	want := `docker run -v gcb-local-vol:/workspace --name gcb-local-vol-helper busybox
docker cp gcb-local-vol-helper:/workspace /wherever/you/want`
	if got != want {
		t.Errorf("Commands didn't match!\n===Want:\n%s\n===Got:\n%s", want, got)
	}
}

func TestCopyTwice(t *testing.T) {
	ctx := context.Background()
	r := newMockRunner(t)
	vol := New(volumeName, r)

	if err := vol.Copy(ctx, "/wherever/you/want"); err != nil {
		t.Errorf("Copy failed: %v", err)
	}
	if err := vol.Copy(ctx, "/wherever/else/you/want"); err != nil {
		t.Errorf("Copy failed: %v", err)
	}

	got := strings.Join(r.commands, "\n")
	want := `docker run -v gcb-local-vol:/workspace --name gcb-local-vol-helper busybox
docker cp /wherever/you/want gcb-local-vol-helper:/workspace
docker cp /wherever/else/you/want gcb-local-vol-helper:/workspace`
	if got != want {
		t.Errorf("Commands didn't match!\n===Want:\n%s\n===Got:\n%s", want, got)
	}
}

func TestClose(t *testing.T) {
	ctx := context.Background()
	r := newMockRunner(t)
	vol := New(volumeName, r)

	if err := vol.Close(ctx); err != nil {
		t.Errorf("Close failed: %v", err)
	}

	got := strings.Join(r.commands, "\n")
	want := fmt.Sprintf("docker volume rm %s", volumeName)
	if got != want {
		t.Errorf("Commands didn't match!\n===Want:\n%s\n===Got:\n%s", want, got)
	}
}

func TestCloseWithHelper(t *testing.T) {
	ctx := context.Background()
	r := newMockRunner(t)
	vol := New(volumeName, r)

	if err := vol.Copy(ctx, "/wherever/you/want"); err != nil {
		t.Errorf("Copy failed: %v", err)
	}

	if err := vol.Close(ctx); err != nil {
		t.Errorf("Close failed: %v", err)
	}

	got := strings.Join(r.commands, "\n")
	want := `docker run -v gcb-local-vol:/workspace --name gcb-local-vol-helper busybox
docker cp /wherever/you/want gcb-local-vol-helper:/workspace
docker rm gcb-local-vol-helper
docker volume rm gcb-local-vol`
	if got != want {
		t.Errorf("Commands didn't match!\n===Want:\n%s\n===Got:\n%s", want, got)
	}
}
