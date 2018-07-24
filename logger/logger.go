// Copyright 2018 Google, Inc. All rights reserved.
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

// Package logger defines a Logger interface to be used by local builder.
package logger

import (
	"io"
)

// Logger encapsulates logging build output.
type Logger interface {
	WriteMainEntry(msg string)
	Close() error
	MakeWriter(prefix string, stepIdx int, stdout bool) io.Writer
}
