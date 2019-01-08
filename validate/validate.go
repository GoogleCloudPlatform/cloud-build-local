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
	"os"
	"path"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode"

	pb "google.golang.org/genproto/googleapis/devtools/cloudbuild/v1"
	"github.com/golang/protobuf/ptypes"
	"github.com/GoogleCloudPlatform/cloud-build-local/subst"
	"github.com/docker/distribution/reference"
)

const (
	// StartStep is a build step WaitFor dependency that is always satisfied.
	StartStep = "-"
	// MaxTimeout is the maximum allowable timeout for a build or build step.
	MaxTimeout = 24 * time.Hour

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
	maxNumSubstitutions = 100       // max number of user-defined substitutions.
	maxSubstKeyLength   = 100       // max length of a substitution key.
	maxSubstValueLength = 4000      // max length of a substitution value.
	maxNumSecretEnvs    = 100       // max number of unique secret env values.
	maxSecretSize       = 64 * 1024 // max size of a secret
	maxArtifactsPaths   = 100       // max number of artifacts paths that can be specified.
	maxNumTags          = 64        // max length of the list of tags.
)

var (
	validUserSubstKeyRE = regexp.MustCompile(`^_[A-Z0-9_]+$`)

	// validBuiltInSubstitutions is the list of valid built-in substitution variables.
	// The boolean values determine if the variable can be used in the
	// --substitutions flag (gcloud / local builder).
	validBuiltInSubstitutions = map[string]bool{
		"PROJECT_ID":  false,
		"BUILD_ID":    false,
		"REPO_NAME":   true,
		"BRANCH_NAME": true,
		"TAG_NAME":    true,
		"REVISION_ID": true,
		"COMMIT_SHA":  true,
		"SHORT_SHA":   true,
	}
	validVolumeNameRE   = regexp.MustCompile("^[a-zA-Z0-9][a-zA-Z0-9_.-]+$")
	reservedVolumePaths = map[string]struct{}{
		"/workspace":           struct{}{},
		"/builder/home":        struct{}{},
		"/var/run/docker.sock": struct{}{},
	}
	// validImageTagRE ensures only proper characters are used in name and tag.
	validImageTagRE = regexp.MustCompile(`^(` + reference.NameRegexp.String() + `(@sha256:` + reference.TagRegexp.String() + `|:` + reference.TagRegexp.String() + `)?)$`)
	// validGCRImageRE ensures proper domain and folder level image for gcr.io. More lenient on the actual characters other than folder structure and domain.
	validGCRImageRE  = regexp.MustCompile(`^([^\.]+\.)?gcr\.io/[^/]+(/[^/]+)+$`)
	validQuayImageRE = regexp.MustCompile(`^(.+\.)?quay\.io/.+$`)
	validBuildTagRE  = regexp.MustCompile(`^(` + reference.TagRegexp.String() + `)$`)
)

// CheckBuild returns no error if build is valid,
// otherwise a descriptive canonical error.
func CheckBuild(b *pb.Build) error {
	if b == nil {
		return errors.New("no build field was provided")
	}

	var buildTimeout time.Duration
	if b.Timeout != nil {
		var err error
		if buildTimeout, err = ptypes.Duration(b.Timeout); err != nil {
			return fmt.Errorf("invalid timeout value: %v", err)
		}
		if buildTimeout > MaxTimeout {
			return fmt.Errorf("timeout exceeds the timeout limit of %v", MaxTimeout)
		}
		if buildTimeout < 0 {
			return errors.New("invalid timeout: timeout must be >=0")
		}
	}

	if err := CheckSubstitutionsLoose(b.Substitutions); err != nil {
		return fmt.Errorf("invalid .substitutions field: %v", err)
	}

	if err := CheckArtifacts(b); err != nil {
		return fmt.Errorf("invalid .artifacts field: %v", err)
	}

	if err := CheckBuildSteps(b.Steps, buildTimeout); err != nil {
		return fmt.Errorf("invalid .steps field: %v", err)
	}

	if missingSubs, err := CheckSubstitutionTemplate(b); err != nil {
		return err
	} else if len(missingSubs) > 0 {
		// If the user doesn't specifically allow loose substitutions, the warnings
		// are returned as an error.
		if b.GetOptions().GetSubstitutionOption() != pb.BuildOptions_ALLOW_LOOSE {
			return fmt.Errorf(strings.Join(missingSubs, ";"))
		}
	}

	if err := checkVolumes(b); err != nil {
		return err
	}

	if err := checkEnvVars(b); err != nil {
		return err
	}

	if err := checkSecrets(b); err != nil {
		return fmt.Errorf("invalid .secrets field: %v", err)
	}

	return nil
}

// CheckBuildAfterSubstitutions returns no error if build is valid,
// otherwise a descriptive canonical error.
func CheckBuildAfterSubstitutions(b *pb.Build) error {
	if err := checkBuildStepNames(b.Steps); err != nil {
		return err
	}

	var err error
	if b.Tags, err = sanitizeBuildTags(b.Tags); err != nil {
		return err
	}

	return checkImageNames(b.Images)
}

// CheckSubstitutions validates the substitutions map.
func CheckSubstitutions(substitutions map[string]string) error {
	if err := CheckSubstitutionsLoose(substitutions); err != nil {
		return err
	}

	// Also check that all the substitions have the user-defined format.
	for k := range substitutions {
		if !validUserSubstKeyRE.MatchString(k) {
			return fmt.Errorf("substitution key %q does not respect format %q", k, validUserSubstKeyRE)
		}
	}

	return nil
}

// CheckSubstitutionsLoose validates the substitutions map, accepting some
// built-in substitutions overrides.
func CheckSubstitutionsLoose(substitutions map[string]string) error {
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
			if overridable, ok := validBuiltInSubstitutions[k]; !ok || !overridable {
				return fmt.Errorf("substitution key %q does not respect format %q and is not an overridable built-in substitutions", k, validUserSubstKeyRE)
			}
		}
		if len(v) > maxSubstValueLength {
			return fmt.Errorf("substitution value %q too long (max: %d)", v, maxSubstValueLength)
		}
	}

	return nil
}

// CheckSubstitutionTemplate checks that all the substitution variables are used
// and all the variables found in the template are used too. It may return an
// error and a list of string warnings.
func CheckSubstitutionTemplate(b *pb.Build) ([]string, error) {
	warnings := []string{}

	// substitutionsUsed is used to check that all the substitution variables
	// are used in the template.
	substitutionsUsed := make(map[string]bool)
	for k := range b.Substitutions {
		substitutionsUsed[k] = false
	}

	checkParameters := func(in string) error {
		parameters := subst.FindTemplateParameters(in)
		for _, p := range parameters {
			if p.Escape {
				continue
			}
			if _, ok := b.Substitutions[p.Key]; !ok {
				if validUserSubstKeyRE.MatchString(p.Key) {
					warnings = append(warnings, fmt.Sprintf("key in the template %q is not matched in the substitution data; substitutions = %+v", p.Key, b.Substitutions))
					continue
				}
				if _, ok := validBuiltInSubstitutions[p.Key]; !ok {
					return fmt.Errorf("key in the template %q is not a valid built-in substitution", p.Key)
				}
			}
			substitutionsUsed[p.Key] = true
		}
		return nil
	}

	for _, step := range b.Steps {
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
	for _, img := range b.Images {
		if err := checkParameters(img); err != nil {
			return warnings, err
		}
	}
	for _, t := range b.Tags {
		if err := checkParameters(t); err != nil {
			return warnings, err
		}
	}

	if b.Artifacts != nil && b.Artifacts.GetObjects() != nil {
		objects := b.Artifacts.Objects
		if err := checkParameters(objects.Location); err != nil {
			return warnings, err
		}
		for _, p := range objects.Paths {
			if err := checkParameters(p); err != nil {
				return warnings, err
			}
		}
	}

	for k, v := range substitutionsUsed {
		if v == false {
			warnings = append(warnings, fmt.Sprintf("key %q in the substitution data is not matched in the template", k))
		}
	}
	return warnings, nil
}

// CheckArtifacts checks the number of images, and images' length are under
// limits. Also copies top-level images to the .artifacts.images sub-field.
func CheckArtifacts(b *pb.Build) error {
	if len(b.Images) > 0 {
		if len(b.GetArtifacts().GetImages()) > 0 && !reflect.DeepEqual(b.GetArtifacts().GetImages(), b.Images) {
			return errors.New("cannot specify different .images and .artifacts.images")
		}
		if b.Artifacts == nil {
			b.Artifacts = &pb.Artifacts{}
		}
		// Copy .images to .artifacts.images.
		b.Artifacts.Images = b.Images
	}

	// Validate .artifacts.images.
	if len(b.GetArtifacts().GetImages()) > maxNumImages {
		return fmt.Errorf("cannot specify more than %d images to build", maxNumImages)
	}
	for ii, i := range b.GetArtifacts().GetImages() {
		if len(i) > MaxImageLength {
			return fmt.Errorf("image %d too long (max: %d)", ii, MaxImageLength)
		}
	}

	// Validate .artifacts.objects.
	if b.Artifacts != nil && b.Artifacts.Objects != nil {
		gcsURL := b.Artifacts.Objects.Location
		if len(gcsURL) == 0 {
			return errors.New(".artifacts.location field is empty")
		}
		if !strings.HasPrefix(gcsURL, "gs://") {
			return fmt.Errorf("invalid .artifacts.location value %q; Google Cloud Storage URLs must begin with 'gs://'", gcsURL)
		}
		if !strings.HasSuffix(gcsURL, "/") {
			// Suffixing the destination bucket URL with a "/" guarantees that the URL will be treated as a directory.
			// For details, see https://cloud.google.com/storage/docs/gsutil/addlhelp/HowSubdirectoriesWork.
			b.Artifacts.Objects.Location = gcsURL + "/"
		}

		if len(b.Artifacts.Objects.Paths) == 0 {
			return errors.New(".artifacts.paths field empty")
		}
		if numPaths := len(b.Artifacts.Objects.Paths); numPaths > maxArtifactsPaths {
			return fmt.Errorf(".artifacts.paths field has length %d, but cannot specify more than %d", numPaths, maxArtifactsPaths)
		}
		pathExists := map[string]bool{}
		duplicates := []string{}
		for _, p := range b.Artifacts.Objects.Paths {
			// Count duplicates.
			if _, ok := pathExists[p]; ok {
				duplicates = append(duplicates, p)
			}
			pathExists[p] = true

			// Paths with whitespace are invalid.
			for _, ch := range p {
				if unicode.IsSpace(ch) {
					return fmt.Errorf(".artifacts.paths %q contains whitespace", p)
				}
			}
		}
		if len(duplicates) > 0 {
			return fmt.Errorf(".artifacts.paths field has duplicate paths; remove duplicates [%s]", strings.Join(duplicates, ", "))
		}
	}

	
	if len(b.GetArtifacts().GetImages()) > 0 {
		b.Images = b.GetArtifacts().GetImages()
	}

	return nil
}

// CheckBuildSteps checks the number of steps, and their content.
func CheckBuildSteps(steps []*pb.BuildStep, buildTimeout time.Duration) error {
	if buildTimeout == 0 {
		buildTimeout = MaxTimeout
	}
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

		if len(s.Dir) > maxDirLength {
			return fmt.Errorf("build step %d dir too long (max: %d)", i, maxDirLength)
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

		if s.Timeout != nil {
			if timeout, err := ptypes.Duration(s.Timeout); err != nil {
				return fmt.Errorf("invalid .timeout in build step #%d: %v", i, err)
			} else if timeout > MaxTimeout {
				return fmt.Errorf("invalid .timeout in build step #%d: build step timeout %v exceeds the timeout limit of %v", i, timeout, MaxTimeout)
			} else if timeout > buildTimeout {
				return fmt.Errorf("invalid .timeout in build step #%d: build step timeout %v must be <= build timeout %v", i, timeout, buildTimeout)
			} else if timeout < 0 {
				return fmt.Errorf("invalid .timeout in build step #%d: build step timeout must be >0", i)
			}
		}
	}

	return nil
}

func checkSecrets(b *pb.Build) error {

	// Collect set of all used secret_envs.
	usedSecretEnvs := map[string]struct{}{}

	// Make sure global secret_env are defined once
	for _, se := range b.GetOptions().GetSecretEnv() {
		if _, found := usedSecretEnvs[se]; found {
			return fmt.Errorf("Build uses global secretEnv %q more than once", se)
		}
		usedSecretEnvs[se] = struct{}{}
	}

	// Make sure global secret_env are not defined in a step
	for i, step := range b.Steps {
		for _, se := range step.SecretEnv {
			if _, found := usedSecretEnvs[se]; found {
				return fmt.Errorf("Step %d uses the global secretEnv %q", i, se)
			}
		}
	}

	// Make sure a step doesn't use the same secret_env twice.
	for i, step := range b.Steps {
		thisStepSecretEnvs := map[string]struct{}{}
		for _, se := range step.SecretEnv {
			usedSecretEnvs[se] = struct{}{}
			if _, found := thisStepSecretEnvs[se]; found {
				return fmt.Errorf("Step %d uses the secretEnv %q more than once", i, se)
			}
			thisStepSecretEnvs[se] = struct{}{}
		}
	}

	// Collect set of all defined secret_envs, and check that secret_envs are not
	// defined by more than one secret. Also check that only one Secret specifies
	// any given KMS key name.
	definedSecretEnvs := map[string]struct{}{}
	definedSecretKeys := map[string]struct{}{}
	for i, sec := range b.Secrets {
		if _, found := definedSecretKeys[sec.KmsKeyName]; found {
			return fmt.Errorf("kmsKeyName %q is used by more than one secret", sec.KmsKeyName)
		}
		definedSecretKeys[sec.KmsKeyName] = struct{}{}

		if len(sec.SecretEnv) == 0 {
			return fmt.Errorf("secret %d defines no secretEnvs", i)
		}
		for k := range sec.SecretEnv {
			if _, found := definedSecretEnvs[k]; found {
				return fmt.Errorf("secretEnv %q is defined more than once", k)
			}
			definedSecretEnvs[k] = struct{}{}
		}
	}
	// Check that all used secret_envs are defined.
	for used := range usedSecretEnvs {
		if _, found := definedSecretEnvs[used]; !found {
			return fmt.Errorf("secretEnv %q is used without being defined", used)
		}
	}
	// Check that all defined secret_envs are used at least once.
	for defined := range definedSecretEnvs {
		if _, found := usedSecretEnvs[defined]; !found {
			return fmt.Errorf("secretEnv %q is defined without being used", defined)
		}
	}
	if len(definedSecretEnvs) > maxNumSecretEnvs {
		return fmt.Errorf("build defines more than %d secret values", maxNumSecretEnvs)
	}

	// Check secret_env max size.
	for _, sec := range b.Secrets {
		for k, v := range sec.SecretEnv {
			if len(v) > maxSecretSize {
				return fmt.Errorf("secretEnv value for %q cannot exceed %dB", k, maxSecretSize)
			}
		}
	}

	globalEnvs := b.Options.GetEnv()

	// Check for conflicts between local + global secrets and local + global envs
	for i, step := range b.Steps {
		envs := map[string]struct{}{}
		for _, e := range append(globalEnvs, step.Env...) {
			// Previous validation ensures that envs include "=".
			k := e[:strings.Index(e, "=")]
			envs[k] = struct{}{}
		}
		for _, se := range append(b.GetOptions().GetSecretEnv(), step.SecretEnv...) {
			if _, found := envs[se]; found {
				return fmt.Errorf("step %d has secret and non-secret env %q", i, se)
			}
		}
	}

	return nil
}

// checkImageTags validates the image tag flag.
func checkImageTags(imageTags []string) error {
	for _, imageTag := range imageTags {
		if !validImageTagRE.MatchString(imageTag) {
			return fmt.Errorf("invalid image tag %q: must match format %q", imageTag, validImageTagRE)
		}
		if !validGCRImageRE.MatchString(imageTag) && !validQuayImageRE.MatchString(imageTag) {
			return fmt.Errorf("invalid image tag %q: must match format %q", imageTag, validGCRImageRE)
		}
	}
	return nil
}

// checkBuildStepNames validates the build step names.
func checkBuildStepNames(steps []*pb.BuildStep) error {
	for _, step := range steps {
		name := step.Name
		if !validImageTagRE.MatchString(name) {
			return fmt.Errorf("invalid build step name %q", name)
		}
	}
	return nil
}

// checkImageNames validates the images.
func checkImageNames(images []string) error {
	for _, image := range images {
		if !validImageTagRE.MatchString(image) {
			// If the lowercased string matches the validImageTag regex, then uppercase letters are invalidating the string.
			// Return an informative error message to the user.
			// Ideally, we could just print out the desired regex or refer to Docker documentation,
			// but validImageTagRE is terribly long, and there is no Docker documentation to point to.
			if validImageTagRE.MatchString(strings.ToLower(image)) {
				return fmt.Errorf("invalid image name %q contains uppercase letters", image)
			}
			return fmt.Errorf("invalid image name %q", image)
		}
	}
	return nil
}

// sanitizeBuildTags validates and sanitizes the tags list.
func sanitizeBuildTags(tags []string) ([]string, error) {
	if len(tags) > maxNumTags {
		return nil, fmt.Errorf("number of tags %d exceeded (max: %d)", len(tags), maxNumTags)
	}

	// Strip empty strings. This might happen as a result of a substitution which
	// has been applied without a value (e.g., $BRANCH_NAME when the source is not
	// from a branch.
	uniques := map[string]bool{}
	cp := []string{}
	for _, t := range tags {
		if t != "" && !uniques[t] {
			cp = append(cp, t)
			uniques[t] = true
		}
	}

	for _, t := range cp {
		if !validBuildTagRE.MatchString(t) {
			return nil, fmt.Errorf("invalid build tag %q: must match format %q", t, validBuildTagRE)
		}
	}
	return cp, nil
}

// checkEnvVars validates global and local env vars
func checkEnvVars(b *pb.Build) error {
	// global env vars
	if err := runCommonEnvChecks(b.GetOptions().GetEnv()); err != nil {
		return fmt.Errorf("invalid .options.env field: %v", err)
	}

	// build step local env vars
	for i, s := range b.GetSteps() {
		if err := runCommonEnvChecks(s.GetEnv()); err != nil {
			return fmt.Errorf("invalid .steps.env field: build step %d %v", i, maxNumEnvs)

		}
	}
	return nil
}

// runCommonEnvChecks performs the checks that are common to global and build
// step local environment variables.
func runCommonEnvChecks(envs []string) error {
	if len(envs) > maxNumEnvs {
		return fmt.Errorf("too many envs (max: %d)", maxNumEnvs)
	}
	for ei, a := range envs {
		if len(a) > maxEnvLength {
			return fmt.Errorf("env %d too long (max: %d)", ei, maxEnvLength)
		}
	}
	return nil
}

// commonVolumeChecks performs the volume validations that are common between
// global and build step local volumes.
func commonVolumeChecks(volumes []*pb.Volume) error {
	volumeNames, volumePaths := map[string]bool{}, map[string]bool{}
	for _, vol := range volumes {

		// Check valid volume name.
		if !validVolumeNameRE.MatchString(vol.Name) {
			return fmt.Errorf("volume name %q does not match %q", vol.Name, validVolumeNameRE.String())
		}

		p := path.Clean(vol.Path)
		// Clean and check valid volume path.
		if !path.IsAbs(path.Clean(p)) {
			return fmt.Errorf("path %q is not valid, must be absolute", vol.Path)
		}

		// Check volume path blacklist.
		if _, found := reservedVolumePaths[p]; found {
			return fmt.Errorf("path %q is reserved", vol.Path)
		}
		// Check volume path doesn't start with /cloudbuild/ to allow future paths.
		if strings.HasPrefix(p, "/cloudbuild/") {
			return fmt.Errorf("volume path %q cannot start with /cloudbuild/", vol.Path)
		}

		// Check volume name uniqueness within the provided array.
		if volumeNames[vol.Name] {
			return fmt.Errorf("volume name %q is defined in more than one volume entry", vol.Name)
		}
		volumeNames[vol.Name] = true

		// Check volume path uniqueness within the provided array.
		if volumePaths[p] {
			return fmt.Errorf("volume path %q is defined in more than one volume entry", p)
		}
		volumePaths[p] = true
	}

	return nil
}

// checkVolumes performs validation for global and local volumes.
func checkVolumes(b *pb.Build) error {
	// If global volumes are used, check if there are at least two steps.
	if len(b.GetOptions().GetVolumes()) > 0 {
		if len(b.GetSteps()) < 2 {
			return fmt.Errorf("Global volumes defined but there are fewer than two steps")
		}
	}

	if err := commonVolumeChecks(b.GetOptions().GetVolumes()); err != nil {
		return fmt.Errorf("invalid .options.volumes: %v", err)
	}

	for i, s := range b.GetSteps() {
		if err := commonVolumeChecks(s.GetVolumes()); err != nil {
			return fmt.Errorf("build step #%d - %q: %v", i, s.Id, err)
		}
	}

	// Check that build step local volumes do not conflict with global volumes
	globalNames := map[string]bool{}
	globalPaths := map[string]bool{}

	for _, vol := range b.GetOptions().GetVolumes() {
		// At this point, global volumes have been checked for uniqueness
		globalNames[vol.GetName()] = true
		globalPaths[vol.GetPath()] = true
	}

	for i, s := range b.GetSteps() {
		for _, vol := range s.GetVolumes() {
			if _, found := globalNames[vol.Name]; found {
				return fmt.Errorf("build step #%d - %q: volume name %q conflicts with global volume", i, s.Id, vol.Name)
			}
			if _, found := globalPaths[vol.Path]; found {
				return fmt.Errorf("build step #%d - %q: volume path %q conflicts with global volume", i, s.Id, vol.Path)
			}
		}
	}

	// Check that all volumes are referenced by at least two steps.
	volumesUsed := map[string]int{} // Maps volume name -> # of times used.
	for _, s := range b.GetSteps() {
		for _, vol := range s.GetVolumes() {
			volumesUsed[vol.GetName()]++
		}
	}

	for volume, used := range volumesUsed {
		if used < 2 {
			return fmt.Errorf("Volume %q is only used by one step", volume)
		}
	}

	return nil
}

// IsDirectory checks if the directory exists.
func IsDirectory(path string) (bool, error) {
	fileInfo, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	mode := fileInfo.Mode()
	return mode.IsDir(), nil
}
