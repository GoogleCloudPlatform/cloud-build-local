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

package buildlog

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"common/common"
)



var (
	fakeTime = time.Unix(1439323141, 78)
)

func TestSingleSink(t *testing.T) {
	t.Parallel()
	log := &BuildLog{}
	sink := log.NewSink("test")

	var outputs []*common.LogEntry
	go func() {
		for output := range sink.Lines {
			output.Time = fakeTime
			outputs = append(outputs, output)
		}
		sink.Result <- nil
	}()

	ow := log.MakeWriter("tag")
	ow.Write([]byte("1\n2\n3"))
	if err := log.Close(); err != nil {
		t.Errorf("error closing log: %v", err)
	}

	expected := []*common.LogEntry{
		{
			Line:  0,
			Label: "tag",
			Text:  "1",
			Time:  fakeTime,
		},
		{
			Line:  1,
			Label: "tag",
			Text:  "2",
			Time:  fakeTime,
		},
		{
			Line:  2,
			Label: "tag",
			Text:  "3",
			Time:  fakeTime,
		},
	}

	if !reflect.DeepEqual(outputs, expected) {
		t.Errorf("output:\n%q\ndid not match expected:\n%q\n", outputs, expected)
	}
}

func TestMultiSink(t *testing.T) {
	t.Parallel()
	log := &BuildLog{}
	sink1 := log.NewSink("test1")
	sink2 := log.NewSink("test2")

	var outputs1 []*common.LogEntry
	go func() {
		for output := range sink1.Lines {
			// Copy output fields into new struct and set the time.
			outputs1 = append(outputs1, &common.LogEntry{
				Line:  output.Line,
				Label: output.Label,
				Text:  output.Text,
				Time:  fakeTime,
			})
		}
		sink1.Result <- nil
	}()

	var outputs2 []*common.LogEntry
	go func() {
		for output := range sink2.Lines {
			// Copy output fields into new struct and set the time.
			outputs2 = append(outputs2, &common.LogEntry{
				Line:  output.Line,
				Label: output.Label,
				Text:  output.Text,
				Time:  fakeTime,
			})
		}
		sink2.Result <- nil
	}()

	ow := log.MakeWriter("tag")
	ow.Write([]byte("1\n2\n3"))
	if err := log.Close(); err != nil {
		t.Errorf("error closing log: %v", err)
	}

	expected := []*common.LogEntry{
		{
			Line:  0,
			Label: "tag",
			Text:  "1",
			Time:  fakeTime,
		},
		{
			Line:  1,
			Label: "tag",
			Text:  "2",
			Time:  fakeTime,
		},
		{
			Line:  2,
			Label: "tag",
			Text:  "3",
			Time:  fakeTime,
		},
	}

	if !reflect.DeepEqual(outputs1, expected) {
		t.Errorf("output1:\n%q\ndid not match expected:\n%q\n", outputs1, expected)
	}
	if !reflect.DeepEqual(outputs2, expected) {
		t.Errorf("output2:\n%q\ndid not match expected:\n%q\n", outputs2, expected)
	}

}

func TestSinkFail(t *testing.T) {
	t.Parallel()
	log := BuildLog{}
	sink1 := log.NewSink("test1")
	sink2 := log.NewSink("test2")

	go func() {
		for range sink1.Lines {
		}
		sink1.Result <- nil
	}()
	someError := errors.New("sink failed to push logs")
	go func() {
		for range sink2.Lines {
		}
		sink2.Result <- someError
	}()

	ow := log.MakeWriter("tag")
	ow.Write([]byte("1\n2\n3"))

	if got, want := log.Close(), someError; got != want {
		t.Errorf("Close error mismatch. got %q, want %q", got, want)
	}
}
