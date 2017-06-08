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
	r := RealRunner{}
	in := strings.NewReader("hello\nworld")
	buf := bytes.Buffer{}
	if err := r.Run([]string{"cat", "-n"}, in, &buf, nil, "/tmp"); err != nil {
		t.Errorf("cat test: run returned error: %v", err)
	}
	if got, want := buf.String(), "     1\thello\n     2\tworld"; got != want {
		t.Errorf("cat test: got %q, want %q", got, want)
	}
	gotErr := r.Run([]string{"foobarbaz123459", "hello", "world"}, nil, nil, nil, "/tmp")
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
	r := RealRunner{}

	// Start a long process.
	go func() {
		if err := r.Run([]string{"sleep", "100"}, nil, nil, nil, ""); err == nil {
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
