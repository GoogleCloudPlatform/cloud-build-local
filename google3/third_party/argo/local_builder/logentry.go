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

// Package logentry contains the structure of a log entry.
package logentry

import (
	"time"
)

// Entry is a single log entry containing 1 line of text.
type Entry struct {
	// Line number for this line of output.
	Line int
	// Label describes the writer generating the log entry. eg, "MAIN" or "3:gcr.io/cloud-builders/some-builder:STDOUT"
	Label string
	// Text is one line of output.
	Text string
	// Time is the time the log line was written.
	Time time.Time
}
