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

package validate

import (
	"math/rand"
	"strings"
	"testing"

	cb "google.golang.org/api/cloudbuild/v1"
)

const (
	projectID  = "valid-project"
	projectNum = int64(12345)
	userID     = int64(67890)
)

func randSeq(n int) string {
	var validChars = []rune("ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_")
	b := make([]rune, n)
	for i := range b {
		b[i] = validChars[rand.Intn(len(validChars))]
	}
	return string(b)
}

func TestCheckSubstitutions(t *testing.T) {
	hugeSubstitutionsMap := make(map[string]string)
	for len(hugeSubstitutionsMap) < maxNumSubstitutions+1 {
		hugeSubstitutionsMap[randSeq(maxSubstKeyLength)] = randSeq(1)
	}

	for _, c := range []struct {
		substitutions map[string]string
		wantErr       bool
	}{{
		substitutions: map[string]string{},
		wantErr:       false,
	}, {
		substitutions: map[string]string{
			"_VARIABLE_1": "variable-value",
		},
		wantErr: false,
	}, {
		substitutions: map[string]string{
			"_1VARIABLE": "value",
		},
		wantErr: false,
	}, {
		substitutions: map[string]string{
			"_VARIABLE": randSeq(maxSubstValueLength + 1),
		},
		wantErr: true,
	}, {
		substitutions: map[string]string{
			randSeq(maxSubstKeyLength + 1): "value",
		},
		wantErr: true,
	}, {
		substitutions: map[string]string{
			"Variable": "value",
		},
		wantErr: true,
	}, {
		substitutions: map[string]string{
			"VAR-1": "value",
		},
		wantErr: true,
	}, {
		substitutions: map[string]string{
			"VARIABLE": "value",
		},
		wantErr: true,
	}, {
		substitutions: hugeSubstitutionsMap,
		wantErr:       true,
	}} {
		if err := CheckSubstitutions(c.substitutions); err == nil && c.wantErr {
			t.Errorf("CheckSubstitutions(%v) did not return error", c.substitutions)
		} else if err != nil && !c.wantErr {
			t.Errorf("CheckSubstitutions(%v) got unexpected error: %v", c.substitutions, err)
		}
	}
}

func TestCheckSubstitutionTemplate(t *testing.T) {
	for _, c := range []struct {
		images        []string
		steps         []*cb.BuildStep
		substitutions map[string]string
		wantErr       bool
	}{{
		steps: []*cb.BuildStep{{Name: "$_FOO"}},
		substitutions: map[string]string{
			"_FOO": "Bar",
		},
		wantErr: false,
	}, {
		steps:   []*cb.BuildStep{{Name: "$$FOO"}},
		wantErr: false,
	}, {
		steps:         []*cb.BuildStep{{Name: "$_FOO"}},
		substitutions: map[string]string{}, // missing substitution
		wantErr:       true,
	}, {
		steps: []*cb.BuildStep{{Name: "Baz"}}, // missing variable in template
		substitutions: map[string]string{
			"_FOO": "Bar",
		},
		wantErr: true,
	}, {
		steps:         []*cb.BuildStep{{Name: "$FOO"}}, // invalid built-in substitution
		substitutions: map[string]string{},
		wantErr:       true,
	}} {
		if err := CheckSubstitutionTemplate(c.images, c.steps, c.substitutions); err == nil && c.wantErr {
			t.Errorf("CheckSubstitutionTemplate(%v,%v,%v) did not return error", c.images, c.steps, c.substitutions)
		} else if err != nil && !c.wantErr {
			t.Errorf("CheckSubstitutionTemplate(%v,%v,%v) got unexpected error: %v", c.images, c.steps, c.substitutions, err)
		}
	}
}

func TestValidateBuild(t *testing.T) {
	testCases := []struct {
		build *cb.Build
		valid bool
	}{{
		build: makeTestBuild("valid-build"),
		valid: true,
	}, {
		build: &cb.Build{
			Id:    "name-only",
			Steps: []*cb.BuildStep{{Name: "foo"}},
		},
		valid: true,
	}, {
		// fails because dir must be a relative path.
		build: &cb.Build{
			Id: "step-absolute-dir",
			Steps: []*cb.BuildStep{{
				Name: "gcr.io/test-argo/dockerize",
				Dir:  "/a/b/c",
			}},
		},
		valid: false,
	}, {
		// fails because dir cannot refer to parent directory.
		build: &cb.Build{
			Id: "step-parent-dir",
			Steps: []*cb.BuildStep{{
				Name: "gcr.io/test-argo/dockerize",
				Dir:  "../b/c",
			}},
		},
		valid: false,
	}, {
		// fails because dir cannot refer to parent directory.
		build: &cb.Build{
			Id: "step-parent-dir2",
			Steps: []*cb.BuildStep{{
				Name: "gcr.io/test-argo/dockerize",
				Dir:  "a/../b/../../c",
			}},
		},
		valid: false,
	}, {
		// fails because Id is startstep.
		build: &cb.Build{
			Id: "startstep-id",
			Steps: []*cb.BuildStep{{
				Name: "gcr.io/test-argo/dockerize",
				Id:   StartStep,
			}},
		},
		valid: false,
	}, {
		build: &cb.Build{
			Id: "check-buildsteps-failure",
		},
		valid: false,
	}, {
		// A completely empty build request should error, but it should not panic.
		build: &cb.Build{},
		valid: false,
	}, {
		build: &cb.Build{
			Id: "bad-env",
			Steps: []*cb.BuildStep{{
				Name: "foo",
				Env:  []string{"foobar"},
			}},
		},
		valid: false,
	}, {
		build: &cb.Build{
			Id: "good-env",
			Steps: []*cb.BuildStep{{
				Name: "foo",
				Env:  []string{"foo=bar"},
			}},
		},
		valid: true,
	}, {
		build: &cb.Build{
			Id: "check-images-failure",
			Steps: []*cb.BuildStep{{
				Name: "okay",
			}},
			Images: manyStrings(maxNumImages + 1),
		},
		valid: false,
	}}
	for _, tc := range testCases {
		b := tc.build
		err := CheckBuild(b)
		if tc.valid {
			if err != nil {
				t.Errorf("CheckBuild(%+v) unexpectedly failed: %v", b, err)
			}
		} else if err == nil {
			t.Errorf("CheckBuild(%+v) got nil error, want an error", b)
		}
	}
}

func TestCheckImages(t *testing.T) {
	for _, c := range []struct {
		images  []string
		wantErr bool
	}{{
		images:  []string{"hello", "world"},
		wantErr: false,
	}, {
		images:  manyStrings(maxNumImages + 1),
		wantErr: true,
	}, {
		images:  []string{strings.Repeat("a", MaxImageLength+1)},
		wantErr: true,
	}} {
		if err := CheckImages(c.images); err == nil && c.wantErr {
			t.Errorf("CheckImages(%v) did not return error", c.images)
		} else if err != nil && !c.wantErr {
			t.Errorf("CheckImages(%v) got unexpected error: %v", c.images, err)
		}
	}
}

func TestCheckBuildSteps(t *testing.T) {
	for _, c := range []struct {
		steps   []*cb.BuildStep
		wantErr bool
	}{{
		steps:   []*cb.BuildStep{{Name: "foo"}},
		wantErr: false,
	}, {
		// serial buildsteps
		steps: []*cb.BuildStep{{
			Name:    "gcr.io/my-project",
			Env:     []string{"FOO=bar", "X=BAZ", "BUZ=78"},
			Args:    []string{"a", "b", "c"},
			Dir:     "a/b/c",
			Id:      "A",
			WaitFor: []string{StartStep},
		}, {
			Name:    "gcr.io/my-project",
			Env:     []string{"FOO=bar", "X=BAZ", "BUZ=78"},
			Args:    []string{"a", "b", "c"},
			Dir:     "a/b/c",
			Id:      "B",
			WaitFor: []string{"A"},
		}, {
			Name:    "gcr.io/my-project",
			Env:     []string{"FOO=bar", "X=BAZ", "BUZ=78"},
			Args:    []string{"a", "b", "c"},
			Dir:     "a/b/c",
			Id:      "C",
			WaitFor: []string{"B"},
		}},
		wantErr: false,
	}, {
		// mixed dependencies buildsteps
		steps: []*cb.BuildStep{{
			Name:    "gcr.io/my-project",
			Id:      "A",
			WaitFor: []string{StartStep},
		}, {
			Name:    "gcr.io/my-project",
			WaitFor: []string{"A"},
		}, {
			Name:    "gcr.io/my-project",
			Id:      "C",
			WaitFor: []string{"A"},
		}},
		wantErr: false,
	}, {
		// multiple startstep buildsteps
		steps: []*cb.BuildStep{{
			Name:    "gcr.io/my-project",
			WaitFor: []string{StartStep},
		}, {
			Name:    "gcr.io/my-project",
			Id:      "A",
			WaitFor: []string{StartStep},
		}},
		wantErr: false,
	}, {
		// multiple dependencies buildsteps
		steps: []*cb.BuildStep{{
			Name:    "gcr.io/my-project",
			Id:      "A",
			WaitFor: []string{StartStep},
		}, {
			Name:    "gcr.io/my-project",
			Id:      "B",
			WaitFor: []string{StartStep},
		}, {
			Name:    "gcr.io/my-project",
			WaitFor: []string{"A", "B"},
		}},
		wantErr: false,
	}, {
		// fails because step depends on a step that is not yet defined.
		steps: []*cb.BuildStep{{
			Name:    "gcr.io/my-project",
			Id:      "A",
			WaitFor: []string{StartStep},
		}, {
			Name:    "gcr.io/my-project",
			Id:      "B",
			WaitFor: []string{"C"},
		}},
		wantErr: true,
	}, {
		// fails because multiple buildsteps share the same Id.
		steps: []*cb.BuildStep{{
			Name:    "gcr.io/my-project",
			Id:      "A",
			WaitFor: []string{StartStep},
		}, {
			Name: "gcr.io/my-project",
			Id:   "A",
		}},
		wantErr: true,
	}, {
		// fails because step depends on a step that is not yet defined.
		steps: []*cb.BuildStep{{
			Name:    "gcr.io/my-project",
			Id:      "A",
			WaitFor: []string{"B"},
		}, {
			Name:    "gcr.io/my-project",
			Id:      "B",
			WaitFor: []string{"A"},
		}},
		wantErr: true,
	}, {
		// fails because no steps
		steps:   nil,
		wantErr: true,
	}, {
		// fails because step missing name
		steps:   []*cb.BuildStep{{}},
		wantErr: true,
	}, {
		// fails because step name too long
		steps: []*cb.BuildStep{{
			Name: strings.Repeat("a", maxStepNameLength+1),
		}},
		wantErr: true,
	}, {
		// fails because too many envs
		steps: []*cb.BuildStep{{
			Name: "okay",
			Env:  manyStrings(maxNumEnvs + 1),
		}},
		wantErr: true,
	}, {
		// fails because env too long
		steps: []*cb.BuildStep{{
			Name: "okay",
			Env:  []string{"a=" + strings.Repeat("b", maxEnvLength)},
		}},
		wantErr: true,
	}, {
		// fails because too many args
		steps: []*cb.BuildStep{{
			Name: "okay",
			Args: manyStrings(maxNumArgs + 1),
		}},
		wantErr: true,
	}, {
		// fails because too many steps
		steps:   manySteps(maxNumSteps + 1),
		wantErr: true,
	}, {
		// fails because arg too long
		steps: []*cb.BuildStep{{
			Name: "okay",
			Args: []string{strings.Repeat("a", maxArgLength+1)},
		}},
		wantErr: true,
	}, {
		// fails because dir too long
		steps: []*cb.BuildStep{{
			Name: "okay",
			Dir:  strings.Repeat("a", maxDirLength+1),
		}},
		wantErr: true,
	}} {
		if err := CheckBuildSteps(c.steps); err == nil && c.wantErr {
			t.Errorf("CheckBuildSteps(%v) did not return error", c.steps)
		} else if err != nil && !c.wantErr {
			t.Errorf("CheckBuildSteps(%v) got unexpected error: %v", c.steps, err)
		}
	}
}

// manySteps returns a slice of n BuildSteps.
func manySteps(n int) []*cb.BuildStep {
	out := []*cb.BuildStep{}
	for i := 0; i < n; i++ {
		out = append(out, &cb.BuildStep{
			Name: "foo",
		})
	}
	return out
}

// manyStrings returns a slice of n strings.
func manyStrings(n int) []string {
	out := []string{}
	for i := 0; i < n; i++ {
		out = append(out, "foo=bar") // valid env.
	}
	return out
}

func makeTestBuild(buildID string) *cb.Build {
	return &cb.Build{
		Id:        buildID,
		ProjectId: projectID,
		Status:    cb.Build_STATUS_UNKNOWN,
		Steps: []*cb.BuildStep{{
			Name: "gcr.io/$PROJECT_ID/my-builder",
			Args: []string{"gcr.io/some/image/tag"},
		}, {
			Name: "gcr.io/$PROJECT_ID/my-builder",
			Args: []string{"gcr.io/some/image/tag2"},
		}},
		Images: []string{"gcr.io/some/image/tag", "gcr.io/some/image/tag2"},
	}
}
