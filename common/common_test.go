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

package common

import (
	"math"
	"reflect"
	"testing"
	"time"
)

func TestBackoff(t *testing.T) {
	var testCases = []struct {
		baseDelay, maxDelay time.Duration
		retries             int
		maxResult           time.Duration
	}{
		{0, 0, 0, 0},

		{0, time.Second, 0, 0},
		{0, time.Second, 1, 0},
		{0, time.Second, 2, 0},

		{time.Second, time.Second, 0, time.Second},
		{time.Second, time.Second, 1, time.Second},
		{time.Second, time.Second, 2, time.Second},

		{time.Second, time.Minute, 0, time.Second},
		{time.Second, time.Minute, 1, time.Duration(1e9 * math.Pow(backoffFactor, 1))},
		{time.Second, time.Minute, 2, time.Duration(1e9 * math.Pow(backoffFactor, 2))},
		{time.Second, time.Minute, 3, time.Duration(1e9 * math.Pow(backoffFactor, 3))},
		{time.Second, time.Minute, 4, time.Duration(1e9 * math.Pow(backoffFactor, 4))},
	}

	for _, test := range testCases {
		backoff := Backoff(test.baseDelay, test.maxDelay, test.retries)
		if backoff < 0 || backoff > test.maxResult {
			t.Errorf("backoff(%v, %v, %v) = %v outside [0, %v]",
				test.baseDelay, test.maxDelay, test.retries,
				backoff, test.maxResult)
		}
	}
}

func TestParseSubstitutionsFlag(t *testing.T) {
	var testCases = []struct {
		input   string
		wantErr bool
		want    map[string]string
	}{{
		input: "_FOO=bar",
		want:  map[string]string{"_FOO": "bar"},
	}, {
		input: "_FOO=",
		want:  map[string]string{"_FOO": ""},
	}, {
		input: "_FOO=bar,_BAR=argo",
		want:  map[string]string{"_FOO": "bar", "_BAR": "argo"},
	}, {
		input: "_FOO=bar, _BAR=argo", // space between the pair
		want:  map[string]string{"_FOO": "bar", "_BAR": "argo"},
	}, {
		input:   "_FOO",
		wantErr: true,
	}}

	for _, test := range testCases {
		got, err := ParseSubstitutionsFlag(test.input)
		if err != nil && !test.wantErr {
			t.Errorf("ParseSubstitutionsFlag returned an unexpected error: %v", err)
		}
		if err == nil && test.wantErr {
			t.Error("ParseSubstitutionsFlag should have returned an error")
		}
		if test.want != nil && !reflect.DeepEqual(got, test.want) {
			t.Errorf("ParseSubstitutionsFlag failed; got %+v, want %+v", got, test.want)
		}
	}
}
