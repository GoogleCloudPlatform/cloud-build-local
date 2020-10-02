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
	"fmt"
	"reflect"
	"testing"

	pb "google.golang.org/genproto/googleapis/devtools/cloudbuild/v1"
)

const (
	projectID       = "my-project"
	buildID         = "build-id"
	repoName        = "my-repo"
	branchName      = "my-branch"
	tagName         = "my-tag"
	longRevisionID  = "abcdefg1234567"
	shortRevisionID = "abcd"
)

func addProjectSourceAndProvenance(b *pb.Build) {
	b.Id = buildID
	b.ProjectId = projectID
	b.Source = &pb.Source{Source: &pb.Source_RepoSource{RepoSource: &pb.RepoSource{
		ProjectId: projectID,
		RepoName:  repoName,
	}}}
	b.SourceProvenance = &pb.SourceProvenance{
		ResolvedRepoSource: &pb.RepoSource{
			ProjectId: projectID,
			RepoName:  repoName,
			Revision:  &pb.RepoSource_CommitSha{CommitSha: longRevisionID},
		},
	}
}

func addBranchName(b *pb.Build) {
	b.GetSource().GetRepoSource().Revision = &pb.RepoSource_BranchName{BranchName: branchName}
}

func addTagName(b *pb.Build) {
	b.GetSource().GetRepoSource().Revision = &pb.RepoSource_TagName{TagName: tagName}
}
func addRevision(b *pb.Build) {
	b.GetSource().GetRepoSource().Revision = &pb.RepoSource_CommitSha{CommitSha: longRevisionID}
}

func setRevisionID(b *pb.Build, revisionID string) {
	b.GetSourceProvenance().GetResolvedRepoSource().Revision = &pb.RepoSource_CommitSha{CommitSha: revisionID}
}

func checkBuild(got, want *pb.Build) error {
	if !reflect.DeepEqual(want.Images, got.Images) {
		return fmt.Errorf("images: got %q, want %q", got.Images, want.Images)
	}
	for i, gs := range got.Steps {
		ws := want.Steps[i]
		if ws.Name != gs.Name {
			return fmt.Errorf("in steps[%d].Name: got %q, want %q", i, gs.Name, ws.Name)
		}
		if !reflect.DeepEqual(ws.Args, gs.Args) {
			return fmt.Errorf("in steps[%d].Args: got %q, want %q", i, gs.Args, ws.Args)
		}
		if !reflect.DeepEqual(ws.Env, gs.Env) {
			return fmt.Errorf("in steps[%d].Env: got %q, want %q", i, gs.Env, ws.Env)
		}
		if ws.Dir != gs.Dir {
			return fmt.Errorf("in steps[%d].Dir: got %q, want %q", i, gs.Dir, ws.Dir)
		}
	}
	if gotTags, wantTags := len(got.Tags), len(want.Tags); gotTags != wantTags {
		return fmt.Errorf("got %d tags, want %d", gotTags, wantTags)
	}
	for i, g := range got.Tags {
		if g != want.Tags[i] {
			return fmt.Errorf("in tags[%d]: got %q, want %q", i, g, want.Tags[i])
		}
	}
	if got.LogsBucket != want.LogsBucket {
		return fmt.Errorf("logsBucket got %q, want %q", got.LogsBucket, want.LogsBucket)
	}
	return nil
}

func TestBranch(t *testing.T) {
	b := &pb.Build{
		Steps: []*pb.BuildStep{{
			Name:       "gcr.io/$PROJECT_ID/my-builder:${BRANCH_NAME}",
			Args:       []string{"gcr.io/$PROJECT_ID/$BRANCH_NAME:${REVISION_ID}"},
			Env:        []string{"REPO_NAME=$REPO_NAME", "BUILD_ID=${BUILD_ID}"},
			Dir:        "let's say... $BRANCH_NAME?",
			Entrypoint: "thisMakesNoSenseBut...$BUILD_ID",
		}},
		Images:     []string{"gcr.io/foo/$BRANCH_NAME:${COMMIT_SHA}"},
		Tags:       []string{"${BRANCH_NAME}", "repo=${REPO_NAME}", "unchanged"},
		LogsBucket: "gs://${PROJECT_ID}/branch/${BRANCH_NAME}",
	}
	addProjectSourceAndProvenance(b)
	addBranchName(b)
	err := SubstituteBuildFields(b)
	if err != nil {
		t.Fatalf("Error while substituting build fields: %v", err)
	}
	if err := checkBuild(b, &pb.Build{
		Steps: []*pb.BuildStep{{
			Name:       "gcr.io/my-project/my-builder:my-branch",
			Args:       []string{"gcr.io/my-project/my-branch:abcdefg1234567"},
			Env:        []string{"REPO_NAME=my-repo", "BUILD_ID=build-id"},
			Dir:        "let's say... my-branch?",
			Entrypoint: "thisMakesNoSenseBut...build_id",
		}},
		Images:     []string{"gcr.io/foo/my-branch:abcdefg1234567"},
		Tags:       []string{"my-branch", "repo=my-repo", "unchanged"},
		LogsBucket: "gs://my-project/branch/my-branch",
	}); err != nil {
		t.Error(err)
	}
}

func TestTag(t *testing.T) {
	b := &pb.Build{
		Steps: []*pb.BuildStep{{
			Name: "gcr.io/$PROJECT_ID/my-builder:$TAG_NAME",
			Args: []string{"gcr.io/$PROJECT_ID/$TAG_NAME:$REVISION_ID"},
			Env:  []string{"REPO_NAME=$REPO_NAME", "BUILD_ID=$BUILD_ID"},
			Dir:  "let's say... $TAG_NAME?",
		}},
		Images: []string{"gcr.io/foo/$TAG_NAME:$COMMIT_SHA"},
		Tags:   []string{"${TAG_NAME}", "repo=${REPO_NAME}", "unchanged"},
	}
	addProjectSourceAndProvenance(b)
	addTagName(b)
	err := SubstituteBuildFields(b)
	if err != nil {
		t.Fatalf("Error while substituting build fields: %v", err)
	}
	if err := checkBuild(b, &pb.Build{
		Steps: []*pb.BuildStep{{
			Name: "gcr.io/my-project/my-builder:my-tag",
			Args: []string{"gcr.io/my-project/my-tag:abcdefg1234567"},
			Env:  []string{"REPO_NAME=my-repo", "BUILD_ID=build-id"},
			Dir:  "let's say... my-tag?",
		}},
		Images: []string{"gcr.io/foo/my-tag:abcdefg1234567"},
		Tags:   []string{"my-tag", "repo=my-repo", "unchanged"},
	}); err != nil {
		t.Error(err)
	}
}

func TestRevision(t *testing.T) {
	b := &pb.Build{
		Steps: []*pb.BuildStep{{
			Name: "gcr.io/$PROJECT_ID/my-builder",
			Args: []string{"gcr.io/$PROJECT_ID/thing:$REVISION_ID"},
			Env:  []string{"REPO_NAME=$REPO_NAME", "BUILD_ID=$BUILD_ID"},
		}},
		Images: []string{"gcr.io/foo/thing"},
		Tags:   []string{"${REVISION_ID}", "repo=${REPO_NAME}", "unchanged"},
	}
	addProjectSourceAndProvenance(b)
	addRevision(b)
	err := SubstituteBuildFields(b)
	if err != nil {
		t.Fatalf("Error while substituting build fields: %v", err)
	}
	if err := checkBuild(b, &pb.Build{
		Steps: []*pb.BuildStep{{
			Name: "gcr.io/my-project/my-builder",
			Args: []string{"gcr.io/my-project/thing:abcdefg1234567"},
			Env:  []string{"REPO_NAME=my-repo", "BUILD_ID=build-id"},
		}},
		Images: []string{"gcr.io/foo/thing"},
		Tags:   []string{"abcdefg1234567", "repo=my-repo", "unchanged"},
	}); err != nil {
		t.Error(err)
	}
}

func TestShortSha(t *testing.T) {
	for _, test := range []struct {
		b          *pb.Build
		want       *pb.Build
		revisionID string
	}{{
		// Length of commit SHA exceeds maxShortShaLength characters.
		b: &pb.Build{
			Steps: []*pb.BuildStep{{
				Name: "gcr.io/$PROJECT_ID/my-builder",
				Args: []string{"gcr.io/$PROJECT_ID/thing:$SHORT_SHA"},
			}},
			Tags: []string{"$SHORT_SHA"},
		},
		want: &pb.Build{
			Steps: []*pb.BuildStep{{
				Name: "gcr.io/my-project/my-builder",
				Args: []string{"gcr.io/my-project/thing:abcdefg"},
			}},
			Tags: []string{"abcdefg"},
		},
		revisionID: longRevisionID,
	}, {
		// Length of commit SHA does not exceed maxShortShaLength characters.
		b: &pb.Build{
			Steps: []*pb.BuildStep{{
				Name: "gcr.io/$PROJECT_ID/my-builder",
				Args: []string{"gcr.io/$PROJECT_ID/thing:$SHORT_SHA"},
			}},
			Tags: []string{"$SHORT_SHA"},
		},
		want: &pb.Build{
			Steps: []*pb.BuildStep{{
				Name: "gcr.io/my-project/my-builder",
				Args: []string{"gcr.io/my-project/thing:abcd"},
			}},
			Tags: []string{"abcd"},
		},
		revisionID: shortRevisionID,
	}} {
		addProjectSourceAndProvenance(test.b)
		setRevisionID(test.b, test.revisionID)
		err := SubstituteBuildFields(test.b)
		if err != nil {
			t.Fatalf("Error while substituting build fields: %v", err)
		}
		if err := checkBuild(test.b, test.want); err != nil {
			t.Error(err)
		}
	}
}

func TestUnknownFields(t *testing.T) {
	b := &pb.Build{
		Steps: []*pb.BuildStep{{
			Name: "gcr.io/$PROJECT_ID/my-builder",
			Args: []string{"gcr.io/$PROJECT_ID/thing:$REVISION_ID"},
			Env:  []string{"REPO_NAME=$REPO_NAME", "BUILD_ID=$BUILD_ID"},
		}},
		Images: []string{"gcr.io/foo/$BRANCH_NAME"},
	}
	addProjectSourceAndProvenance(b)
	if err := SubstituteBuildFields(b); err != nil {
		t.Fatalf("Error while substituting build fields: %v", err)
	}
	if err := checkBuild(b, &pb.Build{
		Steps: []*pb.BuildStep{{
			Name: "gcr.io/my-project/my-builder",
			Args: []string{"gcr.io/my-project/thing:abcdefg1234567"},
			Env:  []string{"REPO_NAME=my-repo", "BUILD_ID=build-id"},
		}},
		Images: []string{"gcr.io/foo/"},
	}); err != nil {
		t.Error(err)
	}
}

// TestUserSubstitutions tests user-defined substitution.
func TestUserSubstitutions(t *testing.T) {
	testCases := []struct {
		desc, template, want string
		substitutions        map[string]string
		wantErr              bool
	}{{
		desc:     "variable should be substituted",
		template: "Hello $_VAR",
		substitutions: map[string]string{
			"_VAR": "World",
		},
		want: "Hello World",
	}, {
		desc:     "only full variable should be substituted",
		template: "Hello $_VAR_FOO, $_VAR",
		substitutions: map[string]string{
			"_VAR": "World",
		},
		want: "Hello , World",
	}, {
		desc:     "variable should be substituted if sticked with a char respecting [^A-Z0-9_]",
		template: "Hello $_VARfoo",
		substitutions: map[string]string{
			"_VAR": "World",
		},
		want: "Hello Worldfoo",
	}, {
		desc:     "curly braced variable should be substituted",
		template: "Hello ${_VAR}_FOO",
		substitutions: map[string]string{
			"_VAR": "World",
		},
		want: "Hello World_FOO",
	}, {
		desc:     "variable should be substituted",
		template: "Hello, 世界  FOO$_VAR",
		substitutions: map[string]string{
			"_VAR": "World",
		},
		want: "Hello, 世界  FOOWorld",
	}, {
		desc:     "variable should be substituted, even if sticked",
		template: `Hello $_VAR$_VAR`,
		substitutions: map[string]string{
			"_VAR": "World",
		},
		want: `Hello WorldWorld`,
	}, {
		desc:     "variable should be substituted, even if preceded by $$",
		template: `$$$_VAR`,
		substitutions: map[string]string{
			"_VAR": "World",
		},
		want: `$World`,
	}, {
		desc:     "escaped variable should not be substituted",
		template: `Hello $${_VAR}_FOO, $_VAR`,
		substitutions: map[string]string{
			"_VAR": "World",
		},
		want: `Hello ${_VAR}_FOO, World`,
	}, {
		desc:     "escaped variable should not be substituted",
		template: `Hello $$$$_VAR $$$_VAR, $_VAR, $$_VAR`,
		substitutions: map[string]string{
			"_VAR": "World",
		},
		want: `Hello $$_VAR $World, World, $_VAR`,
	}, {
		desc:     "escaped variable should not be substituted",
		template: `$$_VAR`,
		substitutions: map[string]string{
			"_VAR": "World",
		},
		want: `$_VAR`,
	}, {
		desc:          "unmatched keys in the template for a built-in substitution will result in an empty string",
		template:      `Hello $BUILTIN_DEFINED_VARIABLE`,
		substitutions: map[string]string{},
		want:          "Hello ",
	}}

	for _, tc := range testCases {
		b := &pb.Build{
			Steps: []*pb.BuildStep{{
				Dir: tc.template,
			}},
			Substitutions: tc.substitutions,
			Tags:          []string{tc.template},
		}
		addProjectSourceAndProvenance(b)
		err := SubstituteBuildFields(b)
		switch {
		case tc.wantErr && err == nil:
			t.Errorf("%q: want error, got none.", tc.desc)
		case !tc.wantErr && err != nil:
			t.Errorf("%q: want no error, got %v.", tc.desc, err)
		}
		if err != nil {
			continue
		}
		if err := checkBuild(b, &pb.Build{
			Steps: []*pb.BuildStep{{
				Dir: tc.want,
			}},
			Tags: []string{tc.want},
		}); err != nil {
			t.Errorf("%q: %v", tc.desc, err)
		}
	}
}

// TestSubstitutions tests that all substitutions are applied.
func TestSubstitutions(t *testing.T) {
	testCases := []struct {
		desc, template, location, want string
		paths                          []string
		substitutions                  map[string]string
		wantErr                        bool
	}{{
		desc:     "variable should be substituted",
		template: "Hello $_VAR $COMMIT_SHA",
		location: "gs://some-bucket/$_VAR/",
		paths:    []string{"**/$_TEST_OUTPUT", "foo.txt"},
		substitutions: map[string]string{
			"_VAR":         "World",
			"_TEST_OUTPUT": "test.xml",
			"COMMIT_SHA":   "my-sha",
		},
		want: "Hello World my-sha",
	}}

	for _, tc := range testCases {
		b := &pb.Build{
			Steps: []*pb.BuildStep{{
				Dir: tc.template,
			}},
			Substitutions: tc.substitutions,
			Tags:          []string{tc.template},
			Artifacts: &pb.Artifacts{
				Objects: &pb.Artifacts_ArtifactObjects{
					Location: tc.location,
					Paths:    tc.paths,
				},
			},
		}
		addProjectSourceAndProvenance(b)
		err := SubstituteBuildFields(b)
		switch {
		case tc.wantErr && err == nil:
			t.Errorf("%q: want error, got none.", tc.desc)
		case !tc.wantErr && err != nil:
			t.Errorf("%q: want no error, got %v.", tc.desc, err)
		}
		if err != nil {
			continue
		}
		if err := checkBuild(b, &pb.Build{
			Steps: []*pb.BuildStep{{
				Dir: tc.want,
			}},
			Tags: []string{tc.want},
			Artifacts: &pb.Artifacts{
				Objects: &pb.Artifacts_ArtifactObjects{
					Location: "gs://some-bucket/World/",
					Paths:    []string{"**/test.xml", "foo.txt"},
				},
			},
		}); err != nil {
			t.Errorf("%q: %v", tc.desc, err)
		}
	}
}

func TestFindTemplateParameters(t *testing.T) {
	input := `\$BAR$FOO$$BAZ$$$_BOO$$$$BAH`
	want := []*TemplateParameter{{
		Start: 1,
		End:   4,
		Key:   "BAR",
	}, {
		Start: 5,
		End:   8,
		Key:   "FOO",
	}, {
		Start:  9,
		End:    10,
		Escape: true,
	}, {
		Start:  14,
		End:    15,
		Escape: true,
	}, {
		Start: 16,
		End:   20,
		Key:   "_BOO",
	}, {
		Start:  21,
		End:    22,
		Escape: true,
	}, {
		Start:  23,
		End:    24,
		Escape: true,
	}}
	got := FindTemplateParameters(input)
	if !reflect.DeepEqual(want, got) {
		t.Errorf("FindTemplateParameters(%s): want %+v, got %+v", input, want, got)
	}
}

func TestFindValidKeyFromIndex(t *testing.T) {
	testCases := []struct {
		input string
		index int
		want  *TemplateParameter
	}{{
		input: `$BAR`,
		index: 0,
		want: &TemplateParameter{
			Start: 0,
			End:   3,
			Key:   "BAR",
		},
	}, {
		input: `$BAR $FOO`,
		index: 5,
		want: &TemplateParameter{
			Start: 5,
			End:   8,
			Key:   "FOO",
		},
	}, {
		input: `$_BAR$FOO`,
		index: 0,
		want: &TemplateParameter{
			Start: 0,
			End:   4,
			Key:   "_BAR",
		},
	}, {
		input: `${BAR}FOO`,
		index: 0,
		want: &TemplateParameter{
			Start: 0,
			End:   5,
			Key:   "BAR",
		},
	}, {
		input: `$BAR}FOO`,
		index: 0,
		want: &TemplateParameter{
			Start: 0,
			End:   3,
			Key:   "BAR",
		},
	}, {
		input: `${BARFOO`,
		index: 0,
		want:  nil,
	}}
	for _, tc := range testCases {
		got := findValidKeyFromIndex(tc.input, tc.index)
		if !reflect.DeepEqual(tc.want, got) {
			t.Errorf("findValidKeyFromIndex(%s, %d): want %+v, got %+v", tc.input, tc.index, tc.want, got)
		}
	}
}
