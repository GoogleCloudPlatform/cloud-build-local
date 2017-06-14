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

// Package validate provides methods to validate a build.
package validate

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"

	cb "google.golang.org/genproto/googleapis/devtools/cloudbuild/v1"
	"github.com/GoogleCloudPlatform/container-builder-local/subst"
)

const (
	// StartStep is a build step WaitFor dependency that is always satisfied.
	StartStep = "-"

	maxNumSteps       = 100  // max number of steps.
	maxStepNameLength = 1000 // max length of step name.
	maxNumEnvs        = 100  // max number of envs per step.
	maxEnvLength      = 1000 // max length of env value.
	maxNumArgs        = 100  // max number of args per step.
	
	maxArgLength = 4000 // max length of arg value.
	maxDirLength = 1000 // max length of dir value.
	maxNumImages = 100  // max number of images.
	// MaxImageLength is the max length of image value. Used in other packages.
	MaxImageLength      = 1000
	maxNumSubstitutions = 100  // max number of user-defined substitutions.
	maxSubstKeyLength   = 100  // max length of a substitution key.
	maxSubstValueLength = 4000 // max length of a substitution value.

	// Name of the permission required to use a key to decrypt data.
	// Documented at https://cloud.google.com/kms/docs/reference/permissions-and-roles
	cloudkmsDecryptPermission = "cloudkms.cryptoKeyVersions.useToDecrypt"
)

var (
	validUserSubstKeyRE   = regexp.MustCompile(`^_[A-Z0-9_]+$`)
	validBuiltInVariables = map[string]struct{}{
		"PROJECT_ID":  struct{}{},
		"BUILD_ID":    struct{}{},
		"REPO_NAME":   struct{}{},
		"BRANCH_NAME": struct{}{},
		"TAG_NAME":    struct{}{},
		"REVISION_ID": struct{}{},
		"COMMIT_SHA":  struct{}{},
	}
)

// CheckBuild returns no error if build is valid,
// otherwise a descriptive canonical error.
func CheckBuild(b *cb.Build) error {
	if b == nil {
		return errors.New("no build field was provided")
	}

	if err := CheckSubstitutions(b.Substitutions); err != nil {
		return fmt.Errorf("invalid .substitutions field: %v", err)
	}

	if err := CheckImages(b.Images); err != nil {
		return fmt.Errorf("invalid .images field: %v", err)
	}

	if err := CheckBuildSteps(b.Steps); err != nil {
		return fmt.Errorf("invalid .steps field: %v", err)
	}

	if missingSubs, err := CheckSubstitutionTemplate(b.Images, b.Steps, b.Substitutions); err != nil {
		return err
	} else if len(missingSubs) > 0 {
		// If the user doesn't specifically allow loose substitutions, the warnings
		// are returned as an error.
		if b.GetOptions().GetSubstitutionOption() != cb.BuildOptions_ALLOW_LOOSE {
			return fmt.Errorf(strings.Join(missingSubs, ";"))
		}
	}
	return nil
}

// CheckSubstitutions validates the substitutions map.
func CheckSubstitutions(substitutions map[string]string) error {
	if substitutions == nil {
		// Callers can request builds without substitutions.
		return nil
	}

	if len(substitutions) > maxNumSubstitutions {
		return fmt.Errorf("number of substitutions %d exceeded (max: %d)", len(substitutions), maxNumSubstitutions)
	}

	for k, v := range substitutions {
		if len(k) > maxSubstKeyLength {
			return fmt.Errorf("substitution key %q too long (max: %d)", k, maxSubstKeyLength)
		}
		if !validUserSubstKeyRE.MatchString(k) {
			return fmt.Errorf("substitution key %q does not respect format %q", k, validUserSubstKeyRE)
		}
		if len(v) > maxSubstValueLength {
			return fmt.Errorf("substitution value %q too long (max: %d)", v, maxSubstValueLength)
		}
	}

	return nil
}

// CheckSubstitutionTemplate checks that all the substitution variables are used
// and all the variables found in the template are used too. It may returns an
// error and a list of string warnings.
func CheckSubstitutionTemplate(images []string, steps []*cb.BuildStep, substitutions map[string]string) ([]string, error) {
	warnings := []string{}

	// substitutionsUsed is used to check that all the substitution variables
	// are used in the template.
	substitutionsUsed := make(map[string]bool)
	for k := range substitutions {
		substitutionsUsed[k] = false
	}

	checkParameters := func(in string) error {
		parameters := subst.FindTemplateParameters(in)
		for _, p := range parameters {
			if p.Escape {
				continue
			}
			if _, ok := substitutions[p.Key]; !ok {
				if validUserSubstKeyRE.MatchString(p.Key) {
					warnings = append(warnings, fmt.Sprintf("key in the template %q is not matched in the substitution data", p.Key))
					continue
				}
				if _, ok := validBuiltInVariables[p.Key]; !ok {
					return fmt.Errorf("key in the template %q is not a valid built-in substitution", p.Key)
				}
			}
			substitutionsUsed[p.Key] = true
		}
		return nil
	}

	for _, step := range steps {
		if err := checkParameters(step.Name); err != nil {
			return warnings, err
		}
		for _, a := range step.Args {
			if err := checkParameters(a); err != nil {
				return warnings, err
			}
		}
		for _, e := range step.Env {
			if err := checkParameters(e); err != nil {
				return warnings, err
			}
		}
		if err := checkParameters(step.Dir); err != nil {
			return warnings, err
		}
		if err := checkParameters(step.Entrypoint); err != nil {
			return warnings, err
		}
	}
	for _, img := range images {
		if err := checkParameters(img); err != nil {
			return warnings, err
		}
	}
	for k, v := range substitutionsUsed {
		if v == false {
			warnings = append(warnings, fmt.Sprintf("key %q in the substitution data is not matched in the template", k))
		}
	}
	return warnings, nil
}

// CheckImages checks the number of images and image's length are under limits.
func CheckImages(images []string) error {
	if len(images) > maxNumImages {
		return fmt.Errorf("cannot specify more than %d images to build", maxNumImages)
	}
	for ii, i := range images {
		if len(i) > MaxImageLength {
			return fmt.Errorf("image %d too long (max: %d)", ii, MaxImageLength)
		}
	}
	return nil
}

// CheckBuildSteps checks the number of steps, and their content.
func CheckBuildSteps(steps []*cb.BuildStep) error {
	// Check that steps are provided and valid.
	if len(steps) == 0 {
		return errors.New("no build steps are specified")
	}
	if len(steps) > maxNumSteps {
		return fmt.Errorf("cannot specify more than %d build steps", maxNumSteps)
	}
	// knownSteps stores the step id and whether a step has been verified.
	// knownSteps is used to track wait_for dependencies as well. If a build step
	// does not exist in the map, an error is returned.
	knownSteps := map[string]bool{
		StartStep: true,
	}
	for i, s := range steps {
		if s.Name == "" {
			return fmt.Errorf("build step %d must specify name", i)
		}
		if len(s.Name) > maxStepNameLength {
			return fmt.Errorf("build step %d name too long (max: %d)", i, maxStepNameLength)
		}

		if len(s.Args) > maxNumArgs {
			return fmt.Errorf("build step %d too many args (max: %d)", i, maxNumArgs)
		}
		for ai, a := range s.Args {
			if len(a) > maxArgLength {
				return fmt.Errorf("build step %d arg %d too long (max: %d)", i, ai, maxArgLength)
			}
		}

		if len(s.Env) > maxNumEnvs {
			return fmt.Errorf("build step %d too many envs (max: %d)", i, maxNumEnvs)
		}
		for ei, a := range s.Env {
			if len(a) > maxEnvLength {
				return fmt.Errorf("build step %d env %d too long (max: %d)", i, ei, maxEnvLength)
			}
		}

		if len(s.Dir) > maxDirLength {
			return fmt.Errorf("build step %d dir too long (max: %d)", i, maxDirLength)
		}
		d := path.Clean(s.Dir)
		if path.IsAbs(d) {
			return errors.New("dir must be a relative path")
		}
		if strings.HasPrefix(d, "..") {
			return errors.New("dir cannot refer to the parent directory")
		}
		for _, dependency := range s.WaitFor {
			if ok := knownSteps[dependency]; !ok {
				if s.Id != "" {
					return fmt.Errorf("build step #%d - %q depends on %q, which has not been defined", i, s.Id, dependency)
				}
				return fmt.Errorf("build step #%d depends on %q, which has not been defined", i, dependency)
			}
		}
		if s.Id != "" {
			if ok := knownSteps[s.Id]; ok {
				return fmt.Errorf("build step #%d - %q: the ID is not unique", i, s.Id)
			}
			if s.Id == StartStep {
				return fmt.Errorf("build step #%d - %q: the ID cannot be %q which is reserved as a dependency for build steps that should run first", i, s.Id, StartStep)
			}
			knownSteps[s.Id] = true
		}
		for _, e := range s.Env {
			if !strings.Contains(e, "=") {
				return fmt.Errorf(`build step #%d - %q: the Env entry %q must be of the form "KEY=VALUE"`, i, s.Id, e)
			}
		}
	}

	return nil
}
