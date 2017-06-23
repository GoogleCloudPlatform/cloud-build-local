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

// Package buildlog implements a pub/sub log that can have multiple
// readers and multiple writers.
package buildlog

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"common/common"
)

const (
	batchTime = 500 * time.Millisecond
)

// A BuildLog captures the stdout/stderr data for a build. Effectively, it
// functions as a pub/sub topic with multiple publishers and multiple
// subscribers.
type BuildLog struct {
	mu       sync.Mutex // protect line, sinks, children
	line     int
	sinks    []*LogSink
	children []*Writer

	BufferMu              sync.Mutex // protect StructuredEntryBuffer, BufferPath, EntryBufferErr
	StructuredEntryBuffer io.WriteCloser
	BufferPath            string
	EntryBufferErr        error
}

func (l *BuildLog) writeEntry(tag string, msg string) {
	l.mu.Lock()
	entry := &common.LogEntry{
		Line:  l.line,
		Label: tag,
		Text:  msg,
		Time:  time.Now().UTC(),
	}
	l.line++
	l.mu.Unlock()
	l.PublishEntry(entry)
}

// PublishEntry publishes a log entry to the sinks.
func (l *BuildLog) PublishEntry(entry *common.LogEntry) {
	l.mu.Lock()
	sinks := l.sinks
	l.mu.Unlock()
	for _, sink := range sinks {
		sink.Lines <- entry
	}
}

// BufferedLogReader returns a reader that can be fed to an HTTP request for logs.
// This method should only be called after the log is closed and all entries
// have been finished, otherwise access to l.entryBuffer can create a data race.
func (l *BuildLog) BufferedLogReader() (io.Reader, error) {
	l.BufferMu.Lock()
	defer l.BufferMu.Unlock()

	if l.BufferPath == "" {
		return nil, errors.New("no buffered log set up")
	}
	if l.EntryBufferErr != nil {
		return nil, fmt.Errorf("error writing to buffered log: %v", l.EntryBufferErr)
	}
	l.EntryBufferErr = l.StructuredEntryBuffer.Close()
	if l.EntryBufferErr != nil {
		return nil, fmt.Errorf("error closing buffered log: %v", l.EntryBufferErr)
	}
	f, err := os.Open(l.BufferPath)
	if err != nil {
		return nil, fmt.Errorf("error opening buffered log reader temp file: %v", err)
	}

	l.StructuredEntryBuffer = nil

	return f, nil
}

// WriteMainEntry is a helper to write an entry under the "MAIN" tag.
func (l *BuildLog) WriteMainEntry(msg string) {
	l.writeEntry("MAIN", msg)
}

// Close the log, allowing subscribers to complete.  It is an error to write
// to the log after closing it.
func (l *BuildLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	log.Print("Closing build log")
	// Flush any partial lines to the log.
	for _, child := range l.children {
		l.mu.Unlock() // child.Flush() will acquire/release mu
		child.Flush()
		l.mu.Lock()
	}
	// Close the log.  Any subsequent log writes will result in an error.
	for _, sink := range l.sinks {
		close(sink.Lines)
	}
	for _, sink := range l.sinks {
		if err := <-sink.Result; err != nil {
			log.Printf("Error closing log %q: %v", sink.Name, err)
			return err
		}
		log.Printf("Closed log: %s", sink.Name)
	}
	return nil
}

// Writer is a struct that encapsulates a single stream writer, eg stdout / stderr.
type Writer struct {
	parent *BuildLog
	buf    bytes.Buffer
	tag    string
}

// MakeWriter creates a new log Writer that implments io.Writer and adds lines
// to the log each time it sees a "\n".
func (l *BuildLog) MakeWriter(tag string) *Writer {
	b := &Writer{
		tag:    tag,
		parent: l,
	}
	l.mu.Lock()
	l.children = append(l.children, b)
	l.mu.Unlock()
	return b
}

func (w *Writer) saveLine(line string) {
	w.parent.writeEntry(w.tag, line)
}

// Write implements io.Writer.
func (w *Writer) Write(data []byte) (int, error) {
	// .Read() and .Write() errors on w.buf are ignored because w.buf is a
	// bytes.Buffer only known to code in this file, and all errors would be
	// caused by bugs in code in this file.
	//
	// Specifically, a write to a bytes.Buffer always succeeds, and a read from
	// bytes.Buffer succeeds with nil error unless it is empty, in which case
	// it returns EOF as an error. Buf, since we are reading only to advance the
	// buffer, and lineData's length necessarily does not exceed the available
	// data, EOF will never be returned.
	//
	// See http://golang.org/pkg/bytes/#Buffer.Read and
	// http://golang.org/pkg/bytes/#Buffer.Write for the error policy.
	_, _ = w.buf.Write(data)

outer:
	for {
		for i, c := range w.buf.Bytes() {
			if c == '\n' {
				lineData := w.buf.Bytes()[:i+1]
				w.saveLine(string(lineData[:len(lineData)-1])) // drop the '\n' character
				_, _ = w.buf.Read(lineData)                    // advances the buffer by that amount
				continue outer
			}
		}
		// got to the end with no newline, eject
		break
	}
	return len(data), nil
}

// Flush handles the scenario where the last log line doesn't have a "\n"
// character.
func (w *Writer) Flush() {
	// log the remainder
	if w.buf.Len() != 0 {
		w.saveLine(w.buf.String())
	}
}

// LogSink is a struct that encapsulates a log sink.
type LogSink struct {
	Name  string
	Lines chan *common.LogEntry
	// We wait on a single message to the `result` channel after flushing all lines to the sink.
	Result      chan error
	HandleBatch func([]*common.LogEntry) error
}

// NewSink creates a new sink and attach it to the BuildLog.
func (l *BuildLog) NewSink(name string) *LogSink {
	sink := &LogSink{
		Name:   name,
		Lines:  make(chan *common.LogEntry, 100000),
		Result: make(chan error, 1),
	}
	l.mu.Lock()
	l.sinks = append(l.sinks, sink)
	l.mu.Unlock()
	return sink
}

// Run consumes the log sink.
func (s *LogSink) Run() {
	err := s.batchInput()
	if err != nil {
		log.Printf("Logging Failure: %v", err)
	}
	s.Result <- err
	if err != nil {
		for range s.Lines {
			// Consume rest of channel.
			// If our channel backs up, the sender might block and the entire build could wedge waiting for that sender.
			// To ensure that the build eventually terminates, we dump all remaining messages.
		}
	}
}

// batchInput consumes from the input channel and batches Entry records into
// batches that are sent together. It also enforces policies such as a maximum
// latency before log messages make it to the wire.  This is the common policy
// code shared by all sinks.
func (s *LogSink) batchInput() error {
	var buf []*common.LogEntry // buffered entries
	batchTick := time.NewTicker(batchTime)
	for {
		select {
		case l, more := <-s.Lines:
			// If more is false, the s.Lines chan has been closed and l will be nil.
			// Flush any buffered log entries and return.
			if !more {
				if buf != nil {
					if err := s.HandleBatch(buf); err != nil {
						return err
					}
				}
				return nil
			}
			// Buffer the log Entry for batched uploading.
			buf = append(buf, l)
		case <-batchTick.C:
			// Every batchTick.C, upload whatever we've buffered.
			if buf != nil {
				if err := s.HandleBatch(buf); err != nil {
					return err
				}
				buf = nil
			}
		}
	}
}

// SetupPrint starts a LogSink that print to sdout.
func (l *BuildLog) SetupPrint() error {
	l.BufferMu.Lock()
	defer l.BufferMu.Unlock()

	sink := l.NewSink("Print")
	sink.HandleBatch = func(batch []*common.LogEntry) error {
		for _, entry := range batch {
			if strings.HasPrefix(entry.Label, "Step #") {
				fmt.Printf("%s\n", entry.Label+": "+entry.Text)
			} else {
				fmt.Printf("%s\n", entry.Text)
			}
		}
		return nil
	}
	go sink.Run()
	return nil
}
