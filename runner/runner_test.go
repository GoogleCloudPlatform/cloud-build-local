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
	if err := r.Run(ctx, []string{"cat", "-n"}, in, &buf, nil); err != nil {
		t.Errorf("cat test: run returned error: %v", err)
	}
	if got, want := buf.String(), "     1\thello\n     2\tworld"; got != want {
		t.Errorf("cat test: got %q, want %q", got, want)
	}
	gotErr := r.Run(ctx, []string{"foobarbaz123459", "hello", "world"}, nil, nil, nil)
	// Wrap the error so that the class name matches.
	gotErr = fmt.Errorf("%v", gotErr)
	wantErr := errors.New("exec: \"foobarbaz123459\": executable file not found in $PATH")
	if !reflect.DeepEqual(gotErr, wantErr) {
		t.Errorf("bad cmd: got error %q, want %q", gotErr, wantErr)
	}
}

func TestRunTimeout(t *testing.T) {
	r := RealRunner{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got := r.Run(ctx, []string{"sleep", "10"}, nil, nil, nil)
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
		got = r.Run(ctx, []string{"sleep", "10"}, nil, nil, nil)
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
