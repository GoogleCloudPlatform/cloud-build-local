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
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	pb "google.golang.org/genproto/googleapis/devtools/cloudbuild/v1"
	duration "github.com/golang/protobuf/ptypes/duration"
)

const (
	projectID  = "valid-project"
	projectNum = int64(12345)
	userID     = int64(67890)
	kmsKeyName = "projects/my-project/locations/global/keyRings/my-key-ring/cryptoKeys/my-crypto-key"
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

func TestCheckSubstitutionsLoose(t *testing.T) {
	for _, c := range []struct {
		substitutions map[string]string
		wantErr       bool
	}{{
		substitutions: map[string]string{
			"REPO_NAME":   "foo",
			"BRANCH_NAME": "foo",
			"TAG_NAME":    "foo",
			"REVISION_ID": "foo",
			"COMMIT_SHA":  "foo",
			"SHORT_SHA":   "foo",
		},
		wantErr: false,
	}, {
		substitutions: map[string]string{
			"PROJECT_ID": "foo",
		},
		wantErr: true,
	}, {
		substitutions: map[string]string{
			"BUILD_ID": "foo",
		},
		wantErr: true,
	}} {
		if err := CheckSubstitutionsLoose(c.substitutions); err == nil && c.wantErr {
			t.Errorf("CheckSubstitutionsLoose(%v) did not return error", c.substitutions)
		} else if err != nil && !c.wantErr {
			t.Errorf("CheckSubstitutionsLoose(%v) got unexpected error: %v", c.substitutions, err)
		}
	}
}

func TestCheckSubstitutionTemplate(t *testing.T) {
	for _, c := range []struct {
		images, tags  []string
		build         *pb.Build
		steps         []*pb.BuildStep
		artifacts     *pb.Artifacts
		substitutions map[string]string
		wantErr       bool
		wantWarnings  int
	}{{
		build: &pb.Build{
			Steps: []*pb.BuildStep{{Name: "$_FOO"}},
			Substitutions: map[string]string{
				"_FOO": "Bar",
			},
		},
		wantWarnings: 0,
	}, {
		build: &pb.Build{
			Steps: []*pb.BuildStep{{Name: "$$FOO"}},
		},
		wantWarnings: 0,
	}, {
		build: &pb.Build{
			Steps:         []*pb.BuildStep{{Name: "$_FOO"}},
			Substitutions: map[string]string{}, // missing substitution
		},
		wantWarnings: 1,
	}, {
		build: &pb.Build{
			Steps: []*pb.BuildStep{{Name: "Baz"}}, // missing variable in template
			Substitutions: map[string]string{
				"_FOO": "Bar",
			},
		},
		wantWarnings: 1,
	}, {
		build: &pb.Build{
			// missing variable in template and missing variable in map
			Steps: []*pb.BuildStep{{Name: "$_BAZ"}},
			Substitutions: map[string]string{
				"_FOO": "Bar",
			},
		},
		wantWarnings: 2,
	}, {
		build: &pb.Build{
			Steps:         []*pb.BuildStep{{Name: "$FOO"}}, // invalid built-in substitution
			Substitutions: map[string]string{},
		},
		wantErr: true,
	}, {
		build: &pb.Build{
			Artifacts: &pb.Artifacts{
				Objects: &pb.Artifacts_ArtifactObjects{
					Location: "gs://some-bucket/$_FOO",
					Paths:    []string{"$_FOO"},
				}},
			Substitutions: map[string]string{
				"_FOO": "Bar",
			},
		},
	}} {
		warnings, err := CheckSubstitutionTemplate(c.build)
		if err == nil && c.wantErr {
			t.Errorf("CheckSubstitutionTemplate(%v,%v,%v) did not return error", c.images, c.steps, c.substitutions)
		} else if err != nil && !c.wantErr {
			t.Errorf("CheckSubstitutionTemplate(%v,%v,%v) got unexpected error: %v", c.images, c.steps, c.substitutions, err)
		}
		if !c.wantErr && len(warnings) != c.wantWarnings {
			t.Errorf("CheckSubstitutionTemplate(%v,%v,%v) did not return the correct number of warnings; got %d, want %d", c.images, c.steps, c.substitutions, len(warnings), c.wantWarnings)
		}
	}
}

func TestValidateBuild(t *testing.T) {
	testCases := []struct {
		build *pb.Build
		valid bool
	}{{
		build: makeTestBuild("valid-build"),
		valid: true,
	}, {
		build: &pb.Build{
			Id:    "name-only",
			Steps: []*pb.BuildStep{{Name: "foo"}},
		},
		valid: true,
	}, {
		// fails because dir must be a relative path.
		build: &pb.Build{
			Id: "step-absolute-dir",
			Steps: []*pb.BuildStep{{
				Name: "test",
				Dir:  "/a/b/c",
			}},
		},
		valid: true,
	}, {
		// fails because dir cannot refer to parent directory.
		build: &pb.Build{
			Id: "step-parent-dir",
			Steps: []*pb.BuildStep{{
				Name: "test",
				Dir:  "../b/c",
			}},
		},
		valid: true,
	}, {
		// fails because dir cannot refer to parent directory.
		build: &pb.Build{
			Id: "step-parent-dir2",
			Steps: []*pb.BuildStep{{
				Name: "test",
				Dir:  "a/../b/../../c",
			}},
		},
		valid: true,
	}, {
		// fails because Id is startstep.
		build: &pb.Build{
			Id: "startstep-id",
			Steps: []*pb.BuildStep{{
				Name: "test",
				Id:   StartStep,
			}},
		},
		valid: false,
	}, {
		build: &pb.Build{
			Id: "check-buildsteps-failure",
		},
		valid: false,
	}, {
		// A completely empty build request should error, but it should not panic.
		build: &pb.Build{},
		valid: false,
	}, {
		build: &pb.Build{
			Id: "bad-env",
			Steps: []*pb.BuildStep{{
				Name: "foo",
				Env:  []string{"foobar"},
			}},
		},
		valid: false,
	}, {
		build: &pb.Build{
			Id: "good-env",
			Steps: []*pb.BuildStep{{
				Name: "foo",
				Env:  []string{"foo=bar"},
			}},
		},
		valid: true,
	}, {
		build: &pb.Build{
			Id: "check-images-failure",
			Steps: []*pb.BuildStep{{
				Name: "okay",
			}},
			Images: manyStrings(maxNumImages + 1),
		},
		valid: false,
	}, {
		build: &pb.Build{
			Id: "check-substitutions-failure",
			Steps: []*pb.BuildStep{{
				Name: "$_UNKNOWN_SUBSTITUTION $_ANOTHER_ONE",
			}},
		},
		valid: false,
	}, {
		build: &pb.Build{
			Id: "check-substitutions-failure",
			Steps: []*pb.BuildStep{{
				Name: "$_UNKNOWN_SUBSTITUTION $_ANOTHER_ONE",
			}},
			Options: &pb.BuildOptions{
				SubstitutionOption: pb.BuildOptions_ALLOW_LOOSE,
			},
		},
		valid: true,
	}, {
		build: &pb.Build{
			Id:      "name-only",
			Steps:   []*pb.BuildStep{{Name: "foo"}},
			Timeout: &duration.Duration{Seconds: int64(MaxTimeout.Seconds())},
		},
		valid: true,
	}, {
		// fails because timeout is too big.
		build: &pb.Build{
			Id:      "name-only",
			Steps:   []*pb.BuildStep{{Name: "foo"}},
			Timeout: &duration.Duration{Seconds: int64(MaxTimeout.Seconds()) + 1},
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

func TestCheckBuildAfterSubstitutions(t *testing.T) {
	testCases := []struct {
		build *pb.Build
		valid bool
	}{{
		build: makeTestBuild("valid-build"),
		valid: true,
	}, {
		build: &pb.Build{
			Steps: []*pb.BuildStep{{Name: "foo"}},
		},
		valid: true,
	}, {
		build: &pb.Build{
			Steps:  []*pb.BuildStep{{Name: "foo"}},
			Images: []string{"foo"},
		},
		valid: true,
	}, {
		build: &pb.Build{
			Steps: []*pb.BuildStep{{Name: "gcr.io/:broken"}},
		},
		valid: false,
	}, {
		build: &pb.Build{
			Steps:  []*pb.BuildStep{{Name: "foo"}},
			Images: []string{"gcr.io/:broken"},
		},
		valid: false,
	}}
	for _, tc := range testCases {
		b := tc.build
		err := CheckBuildAfterSubstitutions(b)
		if tc.valid {
			if err != nil {
				t.Errorf("CheckBuildAfterSubstitutions(%+v) unexpectedly failed: %v", b, err)
			}
		} else if err == nil {
			t.Errorf("CheckBuildAfterSubstitutions(%+v) got nil error, want an error", b)
		}
	}
}

func TestCheckArtifacts(t *testing.T) {
	for _, c := range []struct {
		images    []string
		artifacts *pb.Artifacts
		wantErr   bool
		wantOut   *pb.Build
	}{{
		// Images are propagated from top-level to artifacts.
		images:  []string{"hello", "world"},
		wantErr: false,
		wantOut: &pb.Build{
			Images: []string{"hello", "world"},
			Artifacts: &pb.Artifacts{
				Images: []string{"hello", "world"},
			},
		},
	}, {
		// Images are propagated from artifacts back to top-level.
		artifacts: &pb.Artifacts{
			Images: []string{"hello", "world"},
		},
		wantErr: false,
		wantOut: &pb.Build{
			Images: []string{"hello", "world"},
			Artifacts: &pb.Artifacts{
				Images: []string{"hello", "world"},
			},
		},
	}, {
		// Can't specify different top-level and artifacts.images.
		images: []string{"goodbye", "world"},
		artifacts: &pb.Artifacts{
			Images: []string{"hello", "world"},
		},
		wantErr: true,
	}, {
		// Can specify same top-level and artifacts.images.
		images: []string{"hello", "world"},
		artifacts: &pb.Artifacts{
			Images: []string{"hello", "world"},
		},
		wantErr: false,
		wantOut: &pb.Build{
			Images: []string{"hello", "world"},
			Artifacts: &pb.Artifacts{
				Images: []string{"hello", "world"},
			},
		},
	}, {
		images:  manyStrings(maxNumImages + 1),
		wantErr: true,
	}, {
		images:  []string{strings.Repeat("a", MaxImageLength+1)},
		wantErr: true,
	}, {
		// .artifacts.location not specified.
		artifacts: &pb.Artifacts{
			Objects: &pb.Artifacts_ArtifactObjects{
				Paths: []string{"doesnotmatter"},
			},
		},
		wantErr: true,
	}, {
		// .artifacts.location string does not begin with "gs://"
		artifacts: &pb.Artifacts{
			Objects: &pb.Artifacts_ArtifactObjects{
				Location: "some-bucket",
				Paths:    []string{"doesnotmatter"},
			},
		},
		wantErr: true,
	}, {
		// If .artifacts.location has no trailing slash, suffix a trailing slash.
		artifacts: &pb.Artifacts{
			Objects: &pb.Artifacts_ArtifactObjects{
				Location: "gs://some-bucket",
				Paths:    []string{"doesnotmatter"},
			},
		},
		wantOut: &pb.Build{
			Artifacts: &pb.Artifacts{
				Objects: &pb.Artifacts_ArtifactObjects{
					Location: "gs://some-bucket/",
					Paths:    []string{"doesnotmatter"},
				},
			},
		},
	}, {
		// If .artifacts.location already has a trailing slash, a slash should not be suffixed.
		artifacts: &pb.Artifacts{
			Objects: &pb.Artifacts_ArtifactObjects{
				Location: "gs://some-bucket/",
				Paths:    []string{"doesnotmatter"},
			},
		},
		wantOut: &pb.Build{
			Artifacts: &pb.Artifacts{
				Objects: &pb.Artifacts_ArtifactObjects{
					Location: "gs://some-bucket/",
					Paths:    []string{"doesnotmatter"},
				},
			},
		},
	}, {
		// No .artifacts.paths specified.
		artifacts: &pb.Artifacts{
			Objects: &pb.Artifacts_ArtifactObjects{
				Location: "gs://some-bucket/",
			},
		},
		wantErr: true,
	}, {
		// Length of .artifacts.paths exceeds maximum.
		artifacts: &pb.Artifacts{
			Objects: &pb.Artifacts_ArtifactObjects{
				Location: "gs://some-bucket/",
				Paths:    manyStrings(maxArtifactsPaths + 1),
			},
		},
		wantErr: true,
	}, {
		// Duplicate .artifacts.paths strings.
		artifacts: &pb.Artifacts{
			Objects: &pb.Artifacts_ArtifactObjects{
				Location: "gs://some-bucket/",
				Paths:    []string{"twins.xml", "twins.xml"},
			},
		},
		wantErr: true,
	}} {
		b := &pb.Build{
			Images:    c.images,
			Artifacts: c.artifacts,
		}
		if err := CheckArtifacts(b); err == nil && c.wantErr {
			t.Errorf("CheckArtifacts(%v) did not return error", b)
		} else if err != nil && !c.wantErr {
			t.Errorf("CheckArtifacts(%v) got unexpected error: %v", b, err)
		} else if err == nil && !reflect.DeepEqual(b, c.wantOut) {
			t.Errorf("CheckArtifacts modified build to %+v, want %+v", b, c.wantOut)
		}
	}
}

func TestCheckBuildSteps(t *testing.T) {
	for _, c := range []struct {
		steps   []*pb.BuildStep
		timeout time.Duration
		wantErr bool
	}{{
		steps:   []*pb.BuildStep{{Name: "foo"}},
		wantErr: false,
	}, {
		// serial buildsteps
		steps: []*pb.BuildStep{{
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
		steps: []*pb.BuildStep{{
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
		steps: []*pb.BuildStep{{
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
		steps: []*pb.BuildStep{{
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
		steps: []*pb.BuildStep{{
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
		steps: []*pb.BuildStep{{
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
		steps: []*pb.BuildStep{{
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
		steps:   []*pb.BuildStep{{}},
		wantErr: true,
	}, {
		// fails because step name too long
		steps: []*pb.BuildStep{{
			Name: strings.Repeat("a", maxStepNameLength+1),
		}},
		wantErr: true,
	}, {
		// fails because too many envs
		steps: []*pb.BuildStep{{
			Name: "okay",
			Env:  manyStrings(maxNumEnvs + 1),
		}},
		wantErr: true,
	}, {
		// fails because env too long
		steps: []*pb.BuildStep{{
			Name: "okay",
			Env:  []string{"a=" + strings.Repeat("b", maxEnvLength)},
		}},
		wantErr: true,
	}, {
		// fails because too many args
		steps: []*pb.BuildStep{{
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
		steps: []*pb.BuildStep{{
			Name: "okay",
			Args: []string{strings.Repeat("a", maxArgLength+1)},
		}},
		wantErr: true,
	}, {
		// fails because dir too long
		steps: []*pb.BuildStep{{
			Name: "okay",
			Dir:  strings.Repeat("a", maxDirLength+1),
		}},
		wantErr: true,
	}, {
		// happy build with volumes.
		steps: []*pb.BuildStep{{
			Name:    "okay",
			Volumes: []*pb.Volume{{Name: "myvol", Path: "/foo"}},
		}, {
			Name:    "okay",
			Volumes: []*pb.Volume{{Name: "myvol", Path: "/foo"}},
		}},
	}, {
		// happy build with more volumes.
		steps: []*pb.BuildStep{{
			Name:    "okay",
			Volumes: []*pb.Volume{{Name: "myvol", Path: "/foo"}},
		}, {
			Name:    "okay",
			Volumes: []*pb.Volume{{Name: "myvol", Path: "/foo"}},
		}, {
			Name:    "okay",
			Volumes: []*pb.Volume{{Name: "myvol", Path: "/foo"}},
		}},
	}, {
		// fails because volume isn't used 2+ times
		steps: []*pb.BuildStep{{
			Name:    "okay",
			Volumes: []*pb.Volume{{Name: "myvol", Path: "/foo"}},
		}},
		wantErr: true,
	}, {
		// fails because volume isn't used 2+ times, even when another is
		steps: []*pb.BuildStep{{
			Name:    "okay",
			Volumes: []*pb.Volume{{Name: "myvol", Path: "/foo"}},
		}, {
			Name:    "okay",
			Volumes: []*pb.Volume{{Name: "othervol", Path: "/foo"}},
		}, {
			Name:    "okay",
			Volumes: []*pb.Volume{{Name: "othervol", Path: "/foo"}},
		}},
		wantErr: true,
	}, {
		// fails because volume name is invalid
		steps: []*pb.BuildStep{{
			Name:    "okay",
			Volumes: []*pb.Volume{{Name: "@#()*$@)(*$@", Path: "/foo"}},
		}, {
			Name:    "okay",
			Volumes: []*pb.Volume{{Name: "@#()*$@)(*$@", Path: "/foo"}},
		}},
		wantErr: true,
	}, {
		// fails because volume path is invalid
		steps: []*pb.BuildStep{{
			Name:    "okay",
			Volumes: []*pb.Volume{{Name: "myvol", Path: ")(!*!)($*@#"}},
		}, {
			Name:    "okay",
			Volumes: []*pb.Volume{{Name: "myvol", Path: "/foo"}},
		}},
		wantErr: true,
	}, {
		// fails because volume path is reserved
		steps: []*pb.BuildStep{{
			Name:    "okay",
			Volumes: []*pb.Volume{{Name: "myvol", Path: "/workspace"}},
		}, {
			Name:    "okay",
			Volumes: []*pb.Volume{{Name: "myvol", Path: "/foo"}},
		}},
		wantErr: true,
	}, {
		// fails because volume path starts with /cloudbuild/
		steps: []*pb.BuildStep{{
			Name:    "okay",
			Volumes: []*pb.Volume{{Name: "myvol", Path: "/cloudbuild/foo"}},
		}, {
			Name:    "okay",
			Volumes: []*pb.Volume{{Name: "myvol", Path: "/foo"}},
		}},
		wantErr: true,
	}, {
		// fails because volume path is not absolute
		steps: []*pb.BuildStep{{
			Name:    "okay",
			Volumes: []*pb.Volume{{Name: "myvol", Path: "/absolute"}},
		}, {
			Name:    "okay",
			Volumes: []*pb.Volume{{Name: "myvol", Path: "relative"}},
		}},
		wantErr: true,
	}, {
		// fails because volume name is specified twice in the same step
		steps: []*pb.BuildStep{{
			Name: "okay",
			Volumes: []*pb.Volume{
				{Name: "myvol", Path: "/foo"},
				{Name: "myvol", Path: "/bar"},
			},
		}, {
			Name: "okay",
			Volumes: []*pb.Volume{
				{Name: "myvol", Path: "/foo"},
				{Name: "othervol", Path: "/bar"},
			},
		}},
		wantErr: true,
	}, {
		// fails because volume path is specified twice in the same step
		steps: []*pb.BuildStep{{
			Name: "okay",
			Volumes: []*pb.Volume{
				{Name: "myvol", Path: "/foo"},
				{Name: "othervol", Path: "/foo"},
			},
		}, {
			Name: "okay",
			Volumes: []*pb.Volume{
				{Name: "myvol", Path: "/foo"},
				{Name: "othervol", Path: "/bar"},
			},
		}},
		wantErr: true,
	}, {
		// happy case with per-step timeouts and no build timeout
		steps: []*pb.BuildStep{{Name: "happy-step-timeout", Timeout: &duration.Duration{Seconds: int64(MaxTimeout.Seconds())}}},
	}, {
		// happy case with per-step timeouts and build timeout
		steps:   []*pb.BuildStep{{Name: "happy-step-and-build-timeout", Timeout: &duration.Duration{Seconds: 1}}},
		timeout: 10 * time.Second,
	}, {
		// fails because step timeout > build timeout
		steps:   []*pb.BuildStep{{Name: "unhappy-step-and-build-timeout", Timeout: &duration.Duration{Seconds: 10}}},
		timeout: time.Second,
		wantErr: true,
	}, {
		// fails because step timeout > MaxTimeout
		steps:   []*pb.BuildStep{{Name: "step-timeout-too-big", Timeout: &duration.Duration{Seconds: int64(MaxTimeout.Seconds()) + 1}}},
		wantErr: true,
	}} {
		if err := CheckBuildSteps(c.steps, c.timeout); err == nil && c.wantErr {
			t.Errorf("CheckBuildSteps(%v) did not return error", c.steps)
		} else if err != nil && !c.wantErr {
			t.Errorf("CheckBuildSteps(%v) got unexpected error: %v", c.steps, err)
		}
	}
}

// manySteps returns a slice of n BuildSteps.
func manySteps(n int) []*pb.BuildStep {
	out := []*pb.BuildStep{}
	for i := 0; i < n; i++ {
		out = append(out, &pb.BuildStep{
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

// makeTestBuild should return a valid build after substitutions.
func makeTestBuild(buildID string) *pb.Build {
	return &pb.Build{
		Id:        buildID,
		ProjectId: projectID,
		Status:    pb.Build_STATUS_UNKNOWN,
		Steps: []*pb.BuildStep{{
			Name: "gcr.io/my-project/my-builder",
			Args: []string{"gcr.io/some/image/tag"},
		}, {
			Name: "gcr.io/my-project/my-builder",
			Args: []string{"gcr.io/some/image/tag2"},
		}},
		Images: []string{"gcr.io/some/image/tag", "gcr.io/some/image/tag2"},
	}
}

func TestCheckSecrets(t *testing.T) {
	makeSecretEnvs := func(n int) []string {
		var s []string
		for i := 0; i < n; i++ {
			s = append(s, fmt.Sprintf("MY_SECRET_%d", i))
		}
		return s
	}
	makeSecrets := func(n int) map[string][]byte {
		m := map[string][]byte{}
		for i := 0; i < n; i++ {
			m[fmt.Sprintf("MY_SECRET_%d", i)] = []byte("hunter2")
		}
		return m
	}

	for _, c := range []struct {
		desc    string
		b       *pb.Build
		wantErr error
	}{{
		desc: "Build with no secrets",
		b:    &pb.Build{},
	}, {
		desc: "Build with one secret, used once",
		b: &pb.Build{
			Steps: []*pb.BuildStep{{
				SecretEnv: []string{"MY_SECRET"},
			}},
			Secrets: []*pb.Secret{{
				KmsKeyName: kmsKeyName,
				SecretEnv: map[string][]byte{
					"MY_SECRET": []byte("hunter2"),
				},
			}},
		},
	}, {
		desc: "Build with one secret, never used",
		b: &pb.Build{
			Secrets: []*pb.Secret{{
				KmsKeyName: kmsKeyName,
				SecretEnv: map[string][]byte{
					"MY_SECRET": []byte("hunter2"),
				},
			}},
		},
		wantErr: errors.New(`secretEnv "MY_SECRET" is defined without being used`),
	}, {
		desc: "Build with no secrets, but secret is used",
		b: &pb.Build{
			Steps: []*pb.BuildStep{{
				SecretEnv: []string{"MY_SECRET"},
			}},
		},
		wantErr: errors.New(`secretEnv "MY_SECRET" is used without being defined`),
	}, {
		desc: "Build with secret defined twice with different keys",
		b: &pb.Build{
			Secrets: []*pb.Secret{{
				KmsKeyName: kmsKeyName,
				SecretEnv: map[string][]byte{
					"MY_SECRET": []byte("hunter2"),
				},
			}, {
				KmsKeyName: kmsKeyName + "-2",
				SecretEnv: map[string][]byte{
					"MY_SECRET": []byte("hunter3"),
				},
			}},
		},
		wantErr: errors.New(`secretEnv "MY_SECRET" is defined more than once`),
	}, {
		desc: "Build with secret without any secret_envs",
		b: &pb.Build{
			Secrets: []*pb.Secret{{
				KmsKeyName: kmsKeyName,
			}},
		},
		wantErr: errors.New("secret 0 defines no secretEnvs"),
	}, {
		desc: "Build with secret key defined twice",
		b: &pb.Build{
			Secrets: []*pb.Secret{{
				KmsKeyName: kmsKeyName,
				SecretEnv: map[string][]byte{
					"MY_SECRET": []byte("hunter2"),
				},
			}, {
				KmsKeyName: kmsKeyName,
				SecretEnv: map[string][]byte{
					"ANOTHER_SECRET": []byte("hunter3"),
				},
			}},
		},
		wantErr: errors.New(`kmsKeyName "projects/my-project/locations/global/keyRings/my-key-ring/cryptoKeys/my-crypto-key" is used by more than one secret`),
	}, {
		desc: "Build with secret_env specified twice in the same step",
		b: &pb.Build{
			Steps: []*pb.BuildStep{{
				SecretEnv: []string{"MY_SECRET", "MY_SECRET"},
			}},
			Secrets: []*pb.Secret{{
				KmsKeyName: kmsKeyName,
				SecretEnv: map[string][]byte{
					"MY_SECRET": []byte("hunter2"),
				},
			}},
		},
		wantErr: errors.New(`Step 0 uses the secretEnv "MY_SECRET" more than once`),
	}, {
		desc: "Build with secret value >1 KB",
		b: &pb.Build{
			Steps: []*pb.BuildStep{{
				SecretEnv: []string{"MY_SECRET"},
			}},
			Secrets: []*pb.Secret{{
				KmsKeyName: kmsKeyName,
				SecretEnv: map[string][]byte{
					"MY_SECRET": []byte(strings.Repeat("a", 2000)),
				},
			}},
		},
		wantErr: errors.New(`secretEnv value for "MY_SECRET" cannot exceed 1KB`),
	}, {
		desc: "Happy case: Build with acceptable secret values",
		b: &pb.Build{
			Steps: []*pb.BuildStep{{
				SecretEnv: makeSecretEnvs(maxNumSecretEnvs),
			}},
			Secrets: []*pb.Secret{{
				KmsKeyName: kmsKeyName,
				SecretEnv:  makeSecrets(maxNumSecretEnvs),
			}},
		},
	}, {
		desc: "Build with too many secret values",
		b: &pb.Build{
			Steps: []*pb.BuildStep{{
				SecretEnv: makeSecretEnvs(maxNumSecretEnvs + 1),
			}},
			Secrets: []*pb.Secret{{
				KmsKeyName: kmsKeyName,
				SecretEnv:  makeSecrets(maxNumSecretEnvs + 1),
			}},
		},
		wantErr: errors.New("build defines more than 100 secret values"),
	}, {
		desc: "Step has env and secret_env collision",
		b: &pb.Build{
			Steps: []*pb.BuildStep{{
				Env:       []string{"MY_SECRET=awesome"},
				SecretEnv: []string{"MY_SECRET"},
			}},
			Secrets: []*pb.Secret{{
				KmsKeyName: kmsKeyName,
				SecretEnv: map[string][]byte{
					"MY_SECRET": []byte("hunter2"),
				},
			}},
		},
		wantErr: errors.New(`step 0 has secret and non-secret env "MY_SECRET"`),
	}, {
		desc: "Build has secret and non-secret env in separate steps (which is okay)",
		b: &pb.Build{
			Steps: []*pb.BuildStep{{
				Env: []string{"MY_SECRET=awesome"},
			}, {
				SecretEnv: []string{"MY_SECRET"},
			}},
			Secrets: []*pb.Secret{{
				KmsKeyName: kmsKeyName,
				SecretEnv: map[string][]byte{
					"MY_SECRET": []byte("hunter2"),
				},
			}},
		},
	}} {
		gotErr := checkSecrets(c.b)
		if gotErr == nil && c.wantErr != nil {
			t.Errorf("%s\n got %v, want %v", c.desc, gotErr, c.wantErr)
		} else if gotErr != nil && c.wantErr == nil {
			t.Errorf("%s\n got %v, want %v", c.desc, gotErr, c.wantErr)
		} else if gotErr == nil && c.wantErr == nil {
			// expected
		} else if gotErr.Error() != c.wantErr.Error() {
			t.Errorf("%s\n  got %v\n want %v", c.desc, gotErr, c.wantErr)
		}
	}
}

func TestCheckImageTags(t *testing.T) {
	validTags := []string{
		"subdomain.gcr.io/works/folder/folder",
		"gcr.io/works/folder:tag",
		"gcr.io/works/folder",
		"quay.io/blah/blah:blah",
		"quay.io/blah",
		"sub.quay.io/blah",
		"sub.sub.quay.io/blah",
		"quay.io/blah:blah",
	}
	invalidTags := []string{
		"",
		" ",
		"contains space",
		"_ubuntu",
		"gcr.io/z",
		"gcr.io/broken/noth:",
		"gcr.io/broken:image",
		"subdom.gcr.io/project/image.name.here@digest.here",
		"gcr.io/broken:tag",
		"gcr.io/:broken",
		"gcr.io/projoect/Broken",
		"gcr.o/broken/folder:tag",
		"gcr.io/project/image:foo:bar",
		"gcr.io/project/image@sha257:abcdefg",
		"gcr.io/project/image@sha256:abcdefg:foo",
		"baddomaingcr.io/doesntwork",
		"sub.sub.gcr.io/baddomain/blah",
	}
	for _, tag := range validTags {
		tags := []string{tag}
		if err := checkImageTags(tags); err != nil {
			t.Errorf("checkImageTags(%v) got unexpected error: %v", tags, err)

		}
	}
	for _, tag := range invalidTags {
		tags := []string{tag}
		if err := checkImageTags(tags); err == nil {
			t.Errorf("checkImageTags(%v) did not return error", tags)
		}
	}
}

var validNames = []string{
	"gcr.o/works/folder:tag",
	"gcr.io/z",
	"subdomain.gcr.io/works/folder/folder",
	"gcr.io/works:tag",
	"gcr.io/works/folder:tag",
	"ubuntu",
	"ubuntu:latest",
	"gcr.io/cloud-builders/docker@sha256:blah",
}
var invalidNames = []string{
	"",
	"gcr.io/cloud-builders/docker@sha256:",
	"gcr.io/cloud-builders/docker@sha56:blah",
	"ubnutu::latest",
	"gcr.io/:broken",
	"gcr.io/project/Broken",
}

func TestCheckBuildStepName(t *testing.T) {
	for _, name := range validNames {
		step := &pb.BuildStep{Name: name}
		steps := []*pb.BuildStep{step}
		if err := checkBuildStepNames(steps); err != nil {
			t.Errorf("checkBuildStepNames(%v) got unexpected error: %v", steps, err)
		}
	}
	for _, name := range invalidNames {
		step := &pb.BuildStep{Name: name}
		steps := []*pb.BuildStep{step}
		if err := checkBuildStepNames(steps); err == nil {
			t.Errorf("checkBuildStepNames(%v) did not return error", steps)
		}
	}
}

func TestCheckImageNames(t *testing.T) {
	for _, name := range validNames {
		if err := checkImageNames([]string{name}); err != nil {
			t.Errorf("checkImageNames(%v) got unexpected error: %v", name, err)
		}
	}
	for _, name := range invalidNames {
		if err := checkImageNames([]string{name}); err == nil {
			t.Errorf("checkImageNames(%v) did not return error", name)
		}
	}
}

func TestCheckBuildTags(t *testing.T) {
	var hugeTagList []string
	for i := 0; i < maxNumTags+1; i++ {
		hugeTagList = append(hugeTagList, randSeq(1))
	}

	for _, c := range []struct {
		tags     []string
		wantTags []string
		wantErr  bool
	}{{
		tags:    []string{},
		wantErr: false,
	}, {
		tags:    []string{"ABCabc-._"},
		wantErr: false,
	}, {
		tags:    []string{"_"},
		wantErr: false,
	}, {
		tags:    []string{"%"},
		wantErr: true,
	}, {
		tags:    []string{randSeq(128 + 1)}, // 128 is the max tag length
		wantErr: true,
	}, {
		tags:    hugeTagList,
		wantErr: true,
	}, {
		// strip empty tags
		tags:     []string{""},
		wantTags: []string{},
	}, {
		// strip empty tags
		tags:     []string{"a", "", "b", "", "", "c", ""},
		wantTags: []string{"a", "b", "c"},
	}, {
		// strip duplicates
		tags:     []string{"a", "a", "a"},
		wantTags: []string{"a"},
	}, {
		// strip duplicates and empty tags
		tags:     []string{"a", "", "b", "c", "", "b", "d", "a", "", "e", "b", "a"},
		wantTags: []string{"a", "b", "c", "d", "e"},
	}} {
		got, err := sanitizeBuildTags(c.tags)
		if err == nil && c.wantErr {
			t.Errorf("checkBuildTags(%v) did not return error", c.tags)
		} else if err != nil && !c.wantErr {
			t.Errorf("checkBuildTags(%v) got unexpected error: %v", c.tags, err)
		}

		if c.wantTags != nil && !reflect.DeepEqual(got, c.wantTags) {
			t.Errorf("checkBuildTags(%v) got: %+v, want: %+v", c.tags, got, c.wantTags)
		}
	}
}
