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

// Package subst does string substituion in build fields.
package subst

import (
	"fmt"
	"regexp"

	pb "google.golang.org/genproto/googleapis/devtools/cloudbuild/v1"
)

var (
	unbracedKeyRE   = `([A-Z_][A-Z0-9_]*)`
	bracedKeyRE     = fmt.Sprintf(`{%s}`, unbracedKeyRE)
	validSubstKeyRE = regexp.MustCompile(`^\$(?:` + bracedKeyRE + `|` + unbracedKeyRE + `)`)

	// MaxShortShaLength is the length of the shortsha.
	MaxShortShaLength = 7
)

// SubstituteBuildFields does an in-place string substitution of build parameters.
// User-defined substitutions are also accepted.
// If a built-in substitution value is not defined (perhaps a sourceless build,
// or storage source), then the corresponding substitutions will result in an
// empty string.
func SubstituteBuildFields(b *pb.Build) error {
	repoName := ""
	branchName := ""
	tagName := ""
	commitSHA := ""
	shortSHA := ""

	if s := b.GetSource(); s != nil {
		if rs := s.GetRepoSource(); rs != nil {
			repoName = rs.RepoName
			branchName = rs.GetBranchName()
			tagName = rs.GetTagName()
		}
	}
	if sp := b.GetSourceProvenance(); sp != nil {
		if rrs := sp.GetResolvedRepoSource(); rrs != nil {
			commitSHA = rrs.GetCommitSha()
			shortSHA = commitSHA
			// Length of commit SHA can be less than maxShortShaLength.
			if len(shortSHA) > MaxShortShaLength {
				shortSHA = shortSHA[0:MaxShortShaLength]
			}
		}
	}

	// Built-in substitutions.
	replacements := map[string]string{
		"PROJECT_ID":  b.ProjectId,
		"BUILD_ID":    b.Id,
		"REPO_NAME":   repoName,
		"BRANCH_NAME": branchName,
		"TAG_NAME":    tagName,
		"REVISION_ID": commitSHA,
		"COMMIT_SHA":  commitSHA,
		"SHORT_SHA":   shortSHA,
	}

	// Add user-defined substitutions, overriding built-in substitutions.
	for k, v := range b.Substitutions {
		replacements[k] = v
	}

	applyReplacements := func(in string) string {
		parameters := FindTemplateParameters(in)
		var out []byte
		lastEnd := -1
		for _, p := range parameters {
			out = append(out, in[lastEnd+1:p.Start]...)
			val, ok := replacements[p.Key]
			if !ok {
				val = ""
			}
			// If Escape, `$$` has to be subtituted by `$`
			if p.Escape {
				val = "$"
			}
			out = append(out, []byte(val)...)
			lastEnd = p.End
		}
		out = append(out, in[lastEnd+1:]...)
		return string(out)
	}

	// Apply variable expansion to fields.
	for _, step := range b.Steps {
		step.Name = applyReplacements(step.Name)
		for i, a := range step.Args {
			step.Args[i] = applyReplacements(a)
		}
		for i, e := range step.Env {
			step.Env[i] = applyReplacements(e)
		}
		step.Dir = applyReplacements(step.Dir)
		step.Entrypoint = applyReplacements(step.Entrypoint)
	}
	for i, img := range b.Images {
		b.Images[i] = applyReplacements(img)
	}
	for i, t := range b.Tags {
		b.Tags[i] = applyReplacements(t)
	}
	b.LogsBucket = applyReplacements(b.LogsBucket)
	if b.Artifacts != nil {
		for i, img := range b.Artifacts.Images {
			b.Artifacts.Images[i] = applyReplacements(img)
		}

		if b.Artifacts.Objects != nil {
			b.Artifacts.Objects.Location = applyReplacements(b.Artifacts.Objects.Location)
			for i, p := range b.Artifacts.Objects.Paths {
				b.Artifacts.Objects.Paths[i] = applyReplacements(p)
			}
		}
	}

	return nil
}

// TemplateParameter represents the position of a Key in a string.
type TemplateParameter struct {
	Start, End int
	Key        string
	Escape     bool // if true, this parameter is `$$`
}

// FindTemplateParameters finds all the parameters in the string `input`,
// which are not escaped, and returns an array.
func FindTemplateParameters(input string) []*TemplateParameter {
	parameters := []*TemplateParameter{}
	i := 0
	for i < len(input) {
		// two consecutive $
		if input[i] == '$' && i < len(input)-1 && input[i+1] == '$' {
			p := &TemplateParameter{Start: i, End: i + 1, Escape: true}
			parameters = append(parameters, p)
			i += 2
			continue
		}
		// Unique $
		if input[i] == '$' {
			if p := findValidKeyFromIndex(input, i); p != nil {
				parameters = append(parameters, p)
				i = p.End // continue the search at the end of this parameter.
			}
		}
		i++
	}
	return parameters
}

// findValidKeyFromIndex finds the first valid key starting at index i.
func findValidKeyFromIndex(input string, i int) *TemplateParameter {
	p := &TemplateParameter{Start: i}
	indices := validSubstKeyRE.FindStringSubmatchIndex(input[i:])
	if len(indices) == 0 {
		return nil
	}
	exprIndices := indices[0:2]
	bracedIndices := indices[2:4]
	unbracedIndices := indices[4:6]
	// End of the expression.
	p.End = i + exprIndices[1] - 1
	// Find the not empty match.
	var keyIndices []int
	if bracedIndices[0] != -1 {
		keyIndices = bracedIndices
	} else {
		keyIndices = unbracedIndices
	}
	p.Key = string(input[i+keyIndices[0] : i+keyIndices[1]])
	return p
}
