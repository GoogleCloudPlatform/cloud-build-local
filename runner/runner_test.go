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

package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRun(t *testing.T) {
	ctx := context.Background()
	r := RealRunner{}
	in := strings.NewReader("hello\nworld")
	buf := bytes.Buffer{}
	if err := r.Run(ctx, []string{"cat", "-n"}, in, &buf, nil, "/tmp"); err != nil {
		t.Errorf("cat test: run returned error: %v", err)
	}
	if got, want := buf.String(), "     1\thello\n     2\tworld"; got != want {
		t.Errorf("cat test: got %q, want %q", got, want)
	}
	gotErr := r.Run(ctx, []string{"foobarbaz123459", "hello", "world"}, nil, nil, nil, "/tmp")
	// Wrap the error so that the class name matches.
	gotErr = fmt.Errorf("%v", gotErr)
	wantErr := errors.New("exec: \"foobarbaz123459\": executable file not found in $PATH")
	if !reflect.DeepEqual(gotErr, wantErr) {
		t.Errorf("bad cmd: got error %q, want %q", gotErr, wantErr)
	}
}

func TestMkdirAll(t *testing.T) {
	r := RealRunner{}
	gotErr := r.MkdirAll("")
	// Wrap the error so that the class name matches.
	gotErr = fmt.Errorf("%v", gotErr)
	wantErr := errors.New("mkdir : no such file or directory")
	if !reflect.DeepEqual(gotErr, wantErr) {
		t.Errorf("mkdir: got error %q, want %q", gotErr, wantErr)
	}
}

func TestWriteFile(t *testing.T) {
	r := RealRunner{}
	gotErr := r.WriteFile("/bad/file/path", "file contents")
	// Wrap the error so that the class name matches.
	gotErr = fmt.Errorf("%v", gotErr)
	wantErr := errors.New("open /bad/file/path: no such file or directory")
	if !reflect.DeepEqual(gotErr, wantErr) {
		t.Errorf("WriteFile: got error %q, want %q", gotErr, wantErr)
	}

	tmpDir, err := ioutil.TempDir("", "")
	defer os.RemoveAll(tmpDir) // clean up
	if err != nil {
		t.Fatalf("problem making temp dir: %v", err)
	}
	fname := filepath.Join(tmpDir, "some_file")
	wantContent := "file contents"
	if err := r.WriteFile(fname, wantContent); err != nil {
		t.Errorf("Error writing file: %v", err)
	}
	gotContent, err := ioutil.ReadFile(fname)
	if err != nil {
		t.Errorf("Error reading file back: %v", err)
	}
	if string(gotContent) != wantContent {
		t.Errorf("Content mismatch. got %q, want %q", string(gotContent), wantContent)
	}
}

func TestClean(t *testing.T) {
	ctx := context.Background()
	r := RealRunner{}

	// Start a long process.
	go func() {
		if err := r.Run(ctx, []string{"sleep", "100"}, nil, nil, nil, ""); err == nil {
			t.Error("run should fail when the process is killed")
		}
	}()

	// Wait until the process is started.
	nProcesses := 0
	for nProcesses == 0 {
		time.Sleep(10 * time.Millisecond)
		r.mu.Lock()
		nProcesses = len(r.processes)
		r.mu.Unlock()
	}

	// Clean the processes.
	if err := r.Clean(); err != nil {
		t.Errorf("clean failed: %v", err)
	}
	r.mu.Lock()
	if len(r.processes) != 0 {
		t.Errorf("%d processes running after cleaning", len(r.processes))
	}
	r.mu.Unlock()
}

func TestRunTimeout(t *testing.T) {
	r := RealRunner{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got := r.Run(ctx, []string{"sleep", "10"}, nil, nil, nil, "")
	want := context.DeadlineExceeded
	if got != want {
		t.Errorf("want %v, got %v", want, got)
	}
}

func TestRunCancel(t *testing.T) {
	r := RealRunner{}
	ctx, cancel := context.WithCancel(context.Background())

	var got error
	started := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		close(started)
		got = r.Run(ctx, []string{"sleep", "10"}, nil, nil, nil, "")
		close(finished)
	}()

	// Give r.Run() a chance to start.
	<-started
	time.Sleep(1)
	cancel()

	select {
	case <-finished:
		// Happy case -- context cancel() caused the Run() call to return.
	case <-time.After(time.Second):
		t.Errorf("cancel() didn't cause return from Run().")
	}

	want := context.Canceled
	if got != want {
		t.Errorf("want %v, got %v", want, got)
	}
}
