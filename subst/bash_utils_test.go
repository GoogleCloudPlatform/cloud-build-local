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

package subst

import (
	"reflect"
	"strings"
	"testing"
)

func TestLargeSubstitutionsGetTruncated(t *testing.T) {
	subst := map[string]string{
		"A": "xxxxx",
		"B": "${A//x/${A}}",
		"C": "${B//x/${B}}",
		"D": "${C//x/${C}}",
		"E": "${D//x/${D}}",
		"F": "${E//x/${E}}",
		"G": "${F//x/${F}}",
	}
	got, err := EvalSubstitutions(subst)
	if err != nil {
		t.Fatalf("EvalSubstitutions failed: %v", err)
	}
	gotLengths := map[string]int{}
	for k, v := range got {
		gotLengths[k] = len(v)
	}
	wantLengths := map[string]int{
		"A": 5,
		"B": 25,
		"C": 625,
		"D": maxSubstitutionSize,
		"E": maxSubstitutionSize,
		"F": maxSubstitutionSize,
		"G": maxSubstitutionSize,
	}
	if !reflect.DeepEqual(gotLengths, wantLengths) {
		t.Errorf("failed gotLengths: %+v, want: %+v", gotLengths, wantLengths)
	}
	// Now check that nested string manipulations are truncated.
	// In the following example, the non-truncated string size
	// would be len(A)^5 = 10^5 = 100,000
	subst = map[string]string{
		"A": "xxxxxxxxxx",
		"B": "${A//x/${A//x/${A//x/${A//x/${A}}}}}",
	}
	got, err = EvalSubstitutions(subst)
	if err != nil {
		t.Fatalf("EvalSubstitutions failed: %v", err)
	}
	gotLengths = map[string]int{}
	for k, v := range got {
		gotLengths[k] = len(v)
	}
	wantLengths = map[string]int{
		"A": 10,
		"B": maxSubstitutionSize,
	}
	if !reflect.DeepEqual(gotLengths, wantLengths) {
		t.Errorf("failed gotLengths: %+v, want: %+v", gotLengths, wantLengths)
	}
}

func TestEvalSubstitutions(t *testing.T) {
	for _, tc := range []struct {
		desc     string
		subst    map[string]string
		want     map[string]string
		wantErrs []string
	}{
		{
			desc: "basic replacement without drone/envsubst",
			subst: map[string]string{
				"NO_BRACES":  "basic replacement no braces",
				"_FOO":       "$NO_BRACES",
				"_BAR":       "${HAS_BRACES}",
				"HAS_BRACES": "basic replacement with braces",
			},
			want: map[string]string{
				"_FOO":       "basic replacement no braces",
				"_BAR":       "basic replacement with braces",
				"NO_BRACES":  "basic replacement no braces",
				"HAS_BRACES": "basic replacement with braces",
			},
		},
		{
			desc: "self loop returns error",
			subst: map[string]string{
				"FOO":        "${FOO^^}",
				"UNEFFECTED": "ok here",
			},
			wantErrs: []string{
				"[FOO -> ${FOO^^}]: cycle in evaluating substitutions",
			},
		},
		{
			desc: "default values",
			subst: map[string]string{
				"_MOON":       "${UNDEFINED_VALUE:-${_EARTH}}",
				"_EARTH":      "${_VENUS,}",
				"_VENUS":      "${BRANCH_NAME^^}",
				"BRANCH_NAME": "feature/xtpo",
			},
			want: map[string]string{
				"_MOON":       "fEATURE/XTPO",
				"_EARTH":      "fEATURE/XTPO",
				"_VENUS":      "FEATURE/XTPO",
				"BRANCH_NAME": "feature/xtpo",
			},
		},
		{
			desc: "evaluate list nodes",
			subst: map[string]string{
				"FOO":               "hello",
				"BAR":               "to",
				"BAZ":               "you",
				"COMBINED":          "${FOO} ${BAR} ${BAZ}",
				"COMBINED_REPEATED": "${FOO}+++${FOO}***${FOO} ${FOO}",
				"APPENDED":          "${FOO}navie",
				"WITH_FUNC":         "${FOO^^} navie ${BAZ}",
			},
			want: map[string]string{
				"FOO":               "hello",
				"BAR":               "to",
				"BAZ":               "you",
				"COMBINED":          "hello to you",
				"COMBINED_REPEATED": "hello+++hello***hello hello",
				"APPENDED":          "hellonavie",
				"WITH_FUNC":         "HELLO navie you",
			},
		},
		{
			desc: "chained loop returns error",
			subst: map[string]string{
				"FOO":        "$BAR",
				"BAR":        "$BAZ",
				"BAZ":        "$FOO",
				"RAW_VALUE":  "ok here",
				"UNEFFECTED": "$RAW_VALUE",
			},
			wantErrs: []string{
				"cycle in evaluating substitutions",
			},
		},
		{
			desc: "bash default value honored",
			subst: map[string]string{
				"NOT_BAR": "some value",
				"FOO":     "${BAR:=default}",
			},
			want: map[string]string{
				"FOO":     "default",
				"NOT_BAR": "some value",
			},
		},
		{
			desc: "unknown value evaluates as empty string",
			subst: map[string]string{
				"NOT_BAR": "some value",
				"FOO":     "${BAR:1}",
			},
			want: map[string]string{
				"FOO":     "",
				"NOT_BAR": "some value",
			},
		},
		{
			desc: "envsubst gets called",
			subst: map[string]string{
				"TAG_NAME":     "v0-1-1",
				"DROP_VERSION": "${TAG_NAME:1}",
				"UNDERSCORES":  "${DROP_VERSION//-/_}",
				"UNEFFECTED":   "ok here",
			},
			want: map[string]string{
				"TAG_NAME":     "v0-1-1",
				"DROP_VERSION": "0-1-1",
				"UNDERSCORES":  "0_1_1",
				"UNEFFECTED":   "ok here",
			},
		},
		{
			desc: "substitution in the middle of a string needs curly braces",
			subst: map[string]string{
				"TAG_NAME":          "v0-1-1",
				"REPO_NAME":         "DEMO_REPO",
				"MIDDLE":            "my-app-${TAG_NAME:1}-prod",
				"MULTIPLE":          "some-prefix-${REPO_NAME,,}-tag-${TAG_NAME}-staging",
				"NEED_CURLY_BRACES": "my-app-$TAG_NAME-prod",
			},
			want: map[string]string{
				"TAG_NAME":          "v0-1-1",
				"REPO_NAME":         "DEMO_REPO",
				"MIDDLE":            "my-app-0-1-1-prod",
				"MULTIPLE":          "some-prefix-demo_repo-tag-v0-1-1-staging",
				"NEED_CURLY_BRACES": "my-app-$TAG_NAME-prod",
			},
		},
		{
			desc: "bindings get added to the final list of substitutions",
			subst: map[string]string{
				"tagName":       "v0-1-1",
				"_DROP_VERSION": "${tagName:1}",
				"_UNDERSCORES":  "${_DROP_VERSION//-/_}",
				"_UNEFFECTED":   "ok here",
			},
			want: map[string]string{
				"_DROP_VERSION": "0-1-1",
				"_UNDERSCORES":  "0_1_1",
				"_UNEFFECTED":   "ok here",
				"tagName":       "v0-1-1",
			},
		},
		{
			desc: "evaluate list nodes at depth",
			subst: map[string]string{
				"BRANCH_NAME":            "car409-patch-1",
				"_HEAD_REPO_URL":         "https://github.com/navierula/cloud-builders",
				"_BRANCH_FOR_IMAGE_NAME": "${_BRANCH_NO_PREFIX//-//+}",
				"_BRANCH_NO_PREFIX":      "${BRANCH_NAME#*_}",
				"_IMAGE_NAME":            "my-app-${_BRANCH_FOR_IMAGE_NAME}-prod",
				"_NEW_IMAGE_NAME":        "my-app-${_BRANCH_FOR_IMAGE_NAME//+//-}-prod",
			},
			want: map[string]string{
				"BRANCH_NAME":            "car409-patch-1",
				"_HEAD_REPO_URL":         "https://github.com/navierula/cloud-builders",
				"_BRANCH_FOR_IMAGE_NAME": "car409+patch+1",
				"_BRANCH_NO_PREFIX":      "car409-patch-1",
				"_IMAGE_NAME":            "my-app-car409+patch+1-prod",
				"_NEW_IMAGE_NAME":        "my-app-car409-patch-1-prod",
			},
		},
		{
			desc: "errors get returned",
			subst: map[string]string{
				"_FOO": "${${_BAR}}",
				"_BAZ": "${${_FOO}/sdfg/xcvb}",
			},
			wantErrs: []string{
				"[_FOO -> ${${_BAR}}]: bad substitution",
				"[_BAZ -> ${${_FOO}/sdfg/xcvb}]: bad substitution",
			},
		},
		{
			desc: "error evaluating a chain with a bad substitution",
			subst: map[string]string{
				"_FOO":          "${${_BAR}}",
				"_BAZ":          "${_FOO}-prod",
				"_OK":           "hello",
				"_OK_TRUNCATED": "${_OK:0:2}",
			},
			wantErrs: []string{
				"[_FOO -> ${${_BAR}}]: bad substitution",
			},
		},
	} {
		got, err := EvalSubstitutions(tc.subst)
		if len(tc.wantErrs) == 0 {
			if err != nil {
				t.Errorf("[%s]: EvalSubstitutions failed: %v", tc.desc, err)
			}
		} else {
			if err == nil {
				t.Errorf("[%s]: error missing, want: %+v", tc.desc, tc.wantErrs)
			} else {
				got := err.Error()
				for _, msg := range tc.wantErrs {
					if !strings.Contains(got, msg) {
						t.Errorf("[%s]: unexpected error: got: %+v, want: %+v", tc.desc, err, msg)
					}
				}
			}
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("[%s]: failed got: %+v, want: %+v", tc.desc, got, tc.want)
		}
	}
}
