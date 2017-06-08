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

	cb "google.golang.org/api/cloudbuild/v1"
)

const (
	projectID  = "my-project"
	buildID    = "build-id"
	repoName   = "my-repo"
	branchName = "my-branch"
	tagName    = "my-tag"
	revisionID = "abc123"
)

func addProjectSourceAndProvenance(b *cb.Build) {
	b.Id = buildID
	b.ProjectId = projectID
	b.Source = &cb.Source{Source: &cb.Source_RepoSource{RepoSource: &cb.RepoSource{
		ProjectId: projectID,
		RepoName:  repoName,
	}}}
	b.SourceProvenance = &cb.SourceProvenance{
		ResolvedRepoSource: &cb.RepoSource{
			ProjectId: projectID,
			RepoName:  repoName,
			Revision:  &cb.RepoSource_CommitSha{CommitSha: revisionID},
		},
	}
}

func addBranchName(b *cb.Build) {
	b.GetSource().GetRepoSource().Revision = &cb.RepoSource_BranchName{BranchName: branchName}
}

func addTagName(b *cb.Build) {
	b.GetSource().GetRepoSource().Revision = &cb.RepoSource_TagName{TagName: tagName}
}
func addRevision(b *cb.Build) {
	b.GetSource().GetRepoSource().Revision = &cb.RepoSource_CommitSha{CommitSha: revisionID}
}

func compareBuildStepsAndImages(got, want *cb.Build) error {
	if !reflect.DeepEqual(want.Images, got.Images) {
		return fmt.Errorf("images: want %q, got %q", want.Images, got.Images)
	}
	for i, gs := range got.Steps {
		ws := want.Steps[i]
		if ws.Name != gs.Name {
			return fmt.Errorf("in steps[%d].Name: want %q, got %q", i, ws.Name, gs.Name)
		}
		if !reflect.DeepEqual(ws.Args, gs.Args) {
			return fmt.Errorf("in steps[%d].Args: want %q, got %q", i, ws.Args, gs.Args)
		}
		if !reflect.DeepEqual(ws.Env, gs.Env) {
			return fmt.Errorf("in steps[%d].Env: want %q, got %q", i, ws.Env, gs.Env)
		}
		if ws.Dir != gs.Dir {
			return fmt.Errorf("in steps[%d].Dir: want %q, got %q", i, ws.Dir, gs.Dir)
		}
	}
	return nil
}

func TestBranch(t *testing.T) {
	b := &cb.Build{
		Steps: []*cb.BuildStep{{
			Name:       "gcr.io/$PROJECT_ID/my-builder:${BRANCH_NAME}",
			Args:       []string{"gcr.io/$PROJECT_ID/$BRANCH_NAME:${REVISION_ID}"},
			Env:        []string{"REPO_NAME=$REPO_NAME", "BUILD_ID=${BUILD_ID}"},
			Dir:        "let's say... $BRANCH_NAME?",
			Entrypoint: "thisMakesNoSenseBut...$BUILD_ID",
		}},
		Images: []string{"gcr.io/foo/$BRANCH_NAME:${COMMIT_SHA}"},
	}
	addProjectSourceAndProvenance(b)
	addBranchName(b)
	err := SubstituteBuildFields(b)
	if err != nil {
		t.Fatalf("Error while substituting build fields: %v", err)
	}
	if err := compareBuildStepsAndImages(b, &cb.Build{
		Steps: []*cb.BuildStep{{
			Name:       "gcr.io/my-project/my-builder:my-branch",
			Args:       []string{"gcr.io/my-project/my-branch:abc123"},
			Env:        []string{"REPO_NAME=my-repo", "BUILD_ID=build-id"},
			Dir:        "let's say... my-branch?",
			Entrypoint: "thisMakesNoSenseBut...build_id",
		}},
		Images: []string{"gcr.io/foo/my-branch:abc123"},
	}); err != nil {
		t.Error(err)
	}
}

func TestTag(t *testing.T) {
	b := &cb.Build{
		Steps: []*cb.BuildStep{{
			Name: "gcr.io/$PROJECT_ID/my-builder:$TAG_NAME",
			Args: []string{"gcr.io/$PROJECT_ID/$TAG_NAME:$REVISION_ID"},
			Env:  []string{"REPO_NAME=$REPO_NAME", "BUILD_ID=$BUILD_ID"},
			Dir:  "let's say... $TAG_NAME?",
		}},
		Images: []string{"gcr.io/foo/$TAG_NAME:$COMMIT_SHA"},
	}
	addProjectSourceAndProvenance(b)
	addTagName(b)
	err := SubstituteBuildFields(b)
	if err != nil {
		t.Fatalf("Error while substituting build fields: %v", err)
	}
	if err := compareBuildStepsAndImages(b, &cb.Build{
		Steps: []*cb.BuildStep{{
			Name: "gcr.io/my-project/my-builder:my-tag",
			Args: []string{"gcr.io/my-project/my-tag:abc123"},
			Env:  []string{"REPO_NAME=my-repo", "BUILD_ID=build-id"},
			Dir:  "let's say... my-tag?",
		}},
		Images: []string{"gcr.io/foo/my-tag:abc123"},
	}); err != nil {
		t.Error(err)
	}
}

func TestRevision(t *testing.T) {
	b := &cb.Build{
		Steps: []*cb.BuildStep{{
			Name: "gcr.io/$PROJECT_ID/my-builder",
			Args: []string{"gcr.io/$PROJECT_ID/thing:$REVISION_ID"},
			Env:  []string{"REPO_NAME=$REPO_NAME", "BUILD_ID=$BUILD_ID"},
		}},
		Images: []string{"gcr.io/foo/thing"},
	}
	addProjectSourceAndProvenance(b)
	addRevision(b)
	err := SubstituteBuildFields(b)
	if err != nil {
		t.Fatalf("Error while substituting build fields: %v", err)
	}
	if err := compareBuildStepsAndImages(b, &cb.Build{
		Steps: []*cb.BuildStep{{
			Name: "gcr.io/my-project/my-builder",
			Args: []string{"gcr.io/my-project/thing:abc123"},
			Env:  []string{"REPO_NAME=my-repo", "BUILD_ID=build-id"},
		}},
		Images: []string{"gcr.io/foo/thing"},
	}); err != nil {
		t.Error(err)
	}
}

func TestUnknownFields(t *testing.T) {
	b := &cb.Build{
		Steps: []*cb.BuildStep{{
			Name: "gcr.io/$PROJECT_ID/my-builder",
			Args: []string{"gcr.io/$PROJECT_ID/thing:$REVISION_ID"},
			Env:  []string{"REPO_NAME=$REPO_NAME", "BUILD_ID=$BUILD_ID"},
		}},
		Images: []string{"gcr.io/foo/$BRANCH_NAME"},
	}
	addProjectSourceAndProvenance(b)
	err := SubstituteBuildFields(b)
	if err != nil {
		t.Fatalf("Error while substituting build fields: %v", err)
	}
	if err := compareBuildStepsAndImages(b, &cb.Build{
		Steps: []*cb.BuildStep{{
			Name: "gcr.io/my-project/my-builder",
			Args: []string{"gcr.io/my-project/thing:abc123"},
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
			"_VAR": "Argo",
		},
		want: "Hello Argo",
	}, {
		desc:     "only full variable should be substituted",
		template: "Hello $_VAR_FOO, $_VAR",
		substitutions: map[string]string{
			"_VAR": "Argo",
		},
		want: "Hello , Argo",
	}, {
		desc:     "variable should be substituted if sticked with a char respecting [^A-Z0-9_]",
		template: "Hello $_VARfoo",
		substitutions: map[string]string{
			"_VAR": "Argo",
		},
		want: "Hello Argofoo",
	}, {
		desc:     "curly braced variable should be substituted",
		template: "Hello ${_VAR}_FOO",
		substitutions: map[string]string{
			"_VAR": "Argo",
		},
		want: "Hello Argo_FOO",
	}, {
		desc:     "variable should be substituted",
		template: "Hello, 世界  FOO$_VAR",
		substitutions: map[string]string{
			"_VAR": "Argo",
		},
		want: "Hello, 世界  FOOArgo",
	}, {
		desc:     "variable should be substituted, even if sticked",
		template: `Hello $_VAR$_VAR`,
		substitutions: map[string]string{
			"_VAR": "Argo",
		},
		want: `Hello ArgoArgo`,
	}, {
		desc:     "variable should be substituted, even if preceded by $$",
		template: `$$$_VAR`,
		substitutions: map[string]string{
			"_VAR": "Argo",
		},
		want: `$Argo`,
	}, {
		desc:     "escaped variable should not be substituted",
		template: `Hello $${_VAR}_FOO, $_VAR`,
		substitutions: map[string]string{
			"_VAR": "Argo",
		},
		want: `Hello ${_VAR}_FOO, Argo`,
	}, {
		desc:     "escaped variable should not be substituted",
		template: `Hello $$$$_VAR $$$_VAR, $_VAR, $$_VAR`,
		substitutions: map[string]string{
			"_VAR": "Argo",
		},
		want: `Hello $$_VAR $Argo, Argo, $_VAR`,
	}, {
		desc:     "escaped variable should not be substituted",
		template: `$$_VAR`,
		substitutions: map[string]string{
			"_VAR": "Argo",
		},
		want: `$_VAR`,
	}, {
		desc:          "unmatched keys in the template for a built-in substitution will result in an empty string",
		template:      `Hello $ARGO_DEFINED_VARIABLE`,
		substitutions: map[string]string{},
		want:          "Hello ",
	}}

	for _, tc := range testCases {
		b := &cb.Build{
			Steps: []*cb.BuildStep{{
				Dir: tc.template,
			}},
			Substitutions: tc.substitutions,
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
		if err := compareBuildStepsAndImages(b, &cb.Build{
			Steps: []*cb.BuildStep{{
				Dir: tc.want,
			}},
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
