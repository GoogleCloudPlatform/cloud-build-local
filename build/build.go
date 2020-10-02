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

// Package build implements the logic for executing a single user build.
package build

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	pb "google.golang.org/genproto/googleapis/devtools/cloudbuild/v1"
	"github.com/golang/protobuf/ptypes"

	"github.com/GoogleCloudPlatform/cloud-build-local/common"
	"github.com/GoogleCloudPlatform/cloud-build-local/gsutil"
	"github.com/GoogleCloudPlatform/cloud-build-local/logger"
	"github.com/GoogleCloudPlatform/cloud-build-local/runner"
	"github.com/GoogleCloudPlatform/cloud-build-local/volume"
	"github.com/spf13/afero"
	"google.golang.org/api/cloudkms/v1"
	"golang.org/x/oauth2"
	"github.com/pborman/uuid"
)

const (
	containerWorkspaceDir = "/workspace"

	// homeVolume is the name of the Docker volume that contains the build's
	// $HOME directory, mounted in as /builder/home.
	homeVolume = "homevol"
	homeDir    = "/builder/home"

	// maxPushRetries is the maximum number of times we retry pushing an image
	// in the face of gcr.io DNS lookup errors.
	maxPushRetries = 10

	// builderOutputFile is the name of the file inside each per-step
	// temporary output directory where builders can write arbitrary output
	// information.
	builderOutputFile = "output"

	// stepOutputPath is the path to step output files produced by a step
	// execution. This is the path that's exposed to builders. It's backed by a
	// tempdir in the worker.
	stepOutputPath = "/builder/outputs"

	// maxOutputBytes is the max length of outputs persisted from builder
	// outputs. Data beyond this length is ignored.
	maxOutputBytes = 4 * 1024 // 4 KB

	// osImage identifies an image (1) containing a base OS image that we can use
	// to manipulate files on a Docker volume that (2) we know is either already
	// locally available or is going to be (or very likely to be) pulled anyway
	// for use during the build. We use our docker container image for this
	// purpose.
	osImage = "gcr.io/cloud-builders/docker"
)

var (
	errorScrapingDigest = errors.New("no digest in output")
	// digestPushRE is a regular expression that parses the digest out of the
	// 'docker push' output. For example,
	// "some-tag: digest: sha256:e7e2025236b06b1d8978bffce0cb545613d02f6b7f1959ca6f8d121c93ea3103 size: 9914"
	digestPushRE = regexp.MustCompile(`^(.+):\s*digest:\s*(sha256:[^\s]+)\s*size:\s*\d+$`)
	// digestPullRE is a regular expression that parses the digest out of the
	// 'docker pull' output. For example,
	// "Digest: sha256:070e421b4d5f88e0dd16b1214a47696ab18a7fa2d1f14fbf9ff52799c91df05c"
	digestPullRE = regexp.MustCompile(`^Digest:\s*(sha256:[^\s]+)$`)
	// timeNow is a function that returns the current time; stubbable for testing.
	timeNow = time.Now
	// baseDelay is passed to common.Backoff(); it is only modified in build_test.
	baseDelay = 500 * time.Millisecond
	// maxDelay is passed to common.Backoff(); it is only modified in build_test.
	maxDelay = 10 * time.Second
)

type imageDigest struct {
	tag, digest string
}

// Build manages a single build.
type Build struct {
	// Lock to update public build fields.
	mu               sync.RWMutex
	Request          pb.Build
	HasMultipleSteps bool
	TokenSource      oauth2.TokenSource
	Log              logger.Logger
	status           BuildStatus
	imageDigests     []imageDigest // docker image tag to digest (for built images)
	stepDigests      []string      // build step index to digest (for build steps)
	stepStatus       []pb.Build_Status
	err              error
	Runner           runner.Runner
	Done             chan struct{}
	times            map[BuildStatus]time.Duration
	lastStateStart   time.Time
	idxChan          map[int]chan struct{}
	idxTranslate     map[string]int
	kmsMu            sync.Mutex // guards accesses to kms
	Kms              kms

	// PushErrors tracks how many times a push got retried. This
	// field is not protected by a mutex because the worker will
	// only read it once the build is done, and the build only
	// updates it before the build is done.
	PushErrors     int
	GCRErrors      map[string]int64
	NumSourceBytes int64

	// hostWorkspaceDir is the host workspare directory that will be mounted to
	// the container. It can also be a docker volume.
	hostWorkspaceDir string
	local            bool
	push             bool
	dryrun           bool

	// For UpdateDockerAccessToken, previous GCR auth value to replace.
	prevGCRAuth string

	// timing holds timing information for build execution phases.
	Timing TimingInfo

	// artifacts holds information about uploaded artifacts.
	artifacts ArtifactsInfo

	// gsutilHelper provides helper methods for calling gsutil commands in docker.
	gsutilHelper gsutil.Helper

	// fs is the filesystem to use. In real builds, this is the OS
	// filesystem, in tests it's an in-memory filesystem.
	fs afero.Fs

	// stepOutputs contains builder outputs produced by each step, if any.
	// It is initialized once using initStepOutputsOnce.
	stepOutputs         [][]byte
	initStepOutputsOnce sync.Once
}

// TimingInfo holds timing information for build execution phases.
type TimingInfo struct {
	BuildSteps      []*TimeSpan
	BuildStepPulls  []*TimeSpan
	BuildTotal      *TimeSpan
	ImagePushes     map[string]*TimeSpan
	PushTotal       *TimeSpan // total time to push images and non-container artifacts
	SourceTotal     *TimeSpan
	ArtifactsPushes *TimeSpan // time to push all non-container artifacts
}

// ArtifactsInfo holds information about uploaded artifacts.
type ArtifactsInfo struct {
	ArtifactManifest string
	NumArtifacts     int64
}

// TimeSpan holds the start and end time for a build execution phase.
type TimeSpan struct {
	Start time.Time
	End   time.Time
}

type buildStepTime struct {
	stepIdx  int
	timeSpan *TimeSpan
}

type kms interface {
	Decrypt(key, enc string) (string, error)
}

// New constructs a new Build.
func New(r runner.Runner, b pb.Build, ts oauth2.TokenSource,
	bl logger.Logger, hostWorkspaceDir string, fs afero.Fs, local, push, dryrun bool) *Build {
	bld := &Build{
		Runner:           r,
		Request:          b,
		TokenSource:      ts,
		stepDigests:      make([]string, len(b.Steps)),
		stepStatus:       make([]pb.Build_Status, len(b.Steps)),
		Log:              bl,
		Done:             make(chan struct{}),
		times:            map[BuildStatus]time.Duration{},
		lastStateStart:   timeNow(),
		GCRErrors:        map[string]int64{},
		hostWorkspaceDir: hostWorkspaceDir,
		local:            local,
		push:             push,
		dryrun:           dryrun,
		gsutilHelper:     gsutil.New(r, fs, bl),
		fs:               fs,
	}
	for i := range bld.stepStatus {
		bld.stepStatus[i] = pb.Build_QUEUED
	}
	return bld
}

// Start executes a single build.
func (b *Build) Start(ctx context.Context) {
	if b.local {
		log.Printf("Build id = %v", b.Request.Id)
		defer close(b.Done)
	}

	// Create the home volume.
	homeVol := volume.New(homeVolume, b.Runner)
	if err := homeVol.Setup(ctx); err != nil {
		b.FailBuild(err)
		return
	}
	defer func() {
		// Use a background context; ctx may have been timed out or cancelled.
		if err := homeVol.Close(context.Background()); err != nil {
			log.Printf("Failed to delete homevol: %v", err)
		}
	}()

	if b.Request.GetTimeout() != nil {
		var timeout time.Duration
		var err error
		if timeout, err = ptypes.Duration(b.Request.GetTimeout()); err != nil {
			// Note: we have previously validated the request timeout, so this error
			// should never happen here.
			b.FailBuild(fmt.Errorf("invalid timeout: %v", err))
			return
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	// Fetch and run the build steps.
	b.UpdateStatus(StatusBuild)
	if err := b.runBuildSteps(ctx); err != nil {
		b.FailBuild(err)
		return
	}

	if b.push {
		b.UpdateStatus(StatusPush)
		// Push artifact images and non-container objects.
		if err := b.pushAll(ctx); err != nil {
			b.FailBuild(err)
			return
		}
	}

	// Build is done.
	b.UpdateStatus(StatusDone)
}

// GetStatus returns the build's status in a thread-safe way.
func (b *Build) GetStatus() FullStatus {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return FullStatus{
		BuildStatus: b.status,
		StepStatus:  append([]pb.Build_Status{}, b.stepStatus...),
	}
}

// Summary returns the build's summary in a thread-safe way.
func (b *Build) Summary() BuildSummary {
	b.mu.RLock()
	defer b.mu.RUnlock()
	// Deep-copy pointer values to prevent race conditions if they are later changed.
	var buildTotal, pushTotal, sourceTotal, artifactsPushes *TimeSpan
	if b.Timing.BuildTotal != nil {
		buildTotal = &TimeSpan{}
		*buildTotal = *b.Timing.BuildTotal
	}
	if b.Timing.PushTotal != nil {
		pushTotal = &TimeSpan{}
		*pushTotal = *b.Timing.PushTotal
	}
	if b.Timing.SourceTotal != nil {
		sourceTotal = &TimeSpan{}
		*sourceTotal = *b.Timing.SourceTotal
	}
	if b.Timing.ArtifactsPushes != nil {
		artifactsPushes = &TimeSpan{}
		*artifactsPushes = *b.Timing.ArtifactsPushes
	}
	// Note: no need to deep copy the TimeSpan itself; once the pointer to it is
	// in the map it will never change.
	var imagePushes map[string]*TimeSpan
	if b.Timing.ImagePushes != nil {
		imagePushes = make(map[string]*TimeSpan)
		for k, v := range b.Timing.ImagePushes {
			imagePushes[k] = v
		}
	}
	var buildSteps []*TimeSpan
	if b.Timing.BuildSteps != nil {
		// Deep copy the TimeSpans to prevent a data race where an end-time is
		// written while someone else reads buildSummary.
		for _, t := range b.Timing.BuildSteps {
			if t != nil {
				clone := *t
				buildSteps = append(buildSteps, &clone)
			} else {
				buildSteps = append(buildSteps, nil)
			}
		}
	}

	var buildStepPulls []*TimeSpan
	if b.Timing.BuildStepPulls != nil {
		// Deep copy the TimeSpans to prevent a data race where an
		// end-time is written while someone else reads buildSummary.
		for _, t := range b.Timing.BuildStepPulls {
			if t != nil {
				clone := *t
				buildStepPulls = append(buildStepPulls, &clone)
			} else {
				buildStepPulls = append(buildStepPulls, nil)
			}
		}
	}

	s := BuildSummary{
		Status:          b.status,
		StepStatus:      append([]pb.Build_Status{}, b.stepStatus...),
		BuildStepImages: append([]string{}, b.stepDigests...),
		StepOutputs:     append([][]byte{}, b.stepOutputs...),
		Timing: TimingInfo{
			BuildSteps:      buildSteps,
			BuildStepPulls:  buildStepPulls,
			BuildTotal:      buildTotal,
			ImagePushes:     imagePushes,
			PushTotal:       pushTotal,
			SourceTotal:     sourceTotal,
			ArtifactsPushes: artifactsPushes,
		},
		Artifacts: b.artifacts,
	}

	for _, id := range b.imageDigests {
		s.BuiltImages = append(s.BuiltImages, BuiltImage{
			Name:   id.tag,
			Digest: id.digest,
		})
	}

	return s
}

// Times returns build timings in a thread-safe way.
func (b *Build) Times() map[BuildStatus]time.Duration {
	b.mu.RLock()
	defer b.mu.RUnlock()
	times := map[BuildStatus]time.Duration{}
	for k, v := range b.times {
		times[k] = v
	}
	return times
}

// LastStateStart returns start time of most recent state transition.
func (b *Build) LastStateStart() time.Time {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.lastStateStart
}

// UpdateStatus updates the current status.
func (b *Build) UpdateStatus(status BuildStatus) {
	b.mu.Lock()
	b.times[b.status] = time.Since(b.lastStateStart)
	b.lastStateStart = timeNow()
	b.status = status
	b.mu.Unlock()

	log.Printf("status changed to %q", string(status))
	b.Log.WriteMainEntry(string(status))
}

// FailBuild updates the build's status to failure.
func (b *Build) FailBuild(err error) {
	switch err {
	case context.DeadlineExceeded: // Build timed out.
		b.UpdateStatus(StatusTimeout)
	case context.Canceled: // Build was cancelled.
		b.UpdateStatus(StatusCancelled)
	default: // All other errors are failures.
		b.UpdateStatus(StatusError)
	}
	b.Log.WriteMainEntry("ERROR: " + err.Error())
	b.mu.Lock()
	b.err = err
	b.mu.Unlock()
}

// gcrHosts is the list of GCR hosts that should be logged into when we refresh
// credentials.
var gcrHosts = []string{
	"https://asia.gcr.io",
	"https://b.gcr.io",
	"https://bucket.gcr.io",
	"https://eu.gcr.io",
	"https://gcr.io",
	"https://gcr-staging.sandbox.google.com",
	"https://us.gcr.io",
	"https://marketplace.gcr.io",
	"https://k8s.gcr.io",
}

// SetDockerAccessToken sets the initial Docker config with the credentials we
// use to authorize requests to GCR.
// https://cloud.google.com/container-registry/docs/advanced-authentication#using_an_access_token
func (b *Build) SetDockerAccessToken(ctx context.Context, tok string) error {
	auth := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("oauth2accesstoken:%s", tok)))

	// Construct the minimal ~/.docker/config.json with auth configs for all GCR
	// hosts.
	type authEntry struct {
		Auth string `json:"auth"`
	}
	type config struct {
		Auths map[string]authEntry `json:"auths"`
	}
	cfg := config{map[string]authEntry{}}
	for _, host := range gcrHosts {
		cfg.Auths[host] = authEntry{auth}
	}
	configJSON, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	// Simply run an osImage container with $HOME mounted that writes the ~/.docker/config.json file.
	var buf bytes.Buffer
	args := []string{"docker", "run",
		"--name", fmt.Sprintf("cloudbuild_set_docker_token_%s", uuid.New()),
		"--rm",
		// Mount in the home volume.
		"--volume", homeVolume + ":" + homeDir,
		// Make /builder/home $HOME.
		"--env", "HOME=" + homeDir,
		// Make sure the container uses the correct docker daemon.
		"--volume", "/var/run/docker.sock:/var/run/docker.sock",
		"--entrypoint", "bash",
		osImage,
		"-c", "mkdir -p ~/.docker/ && cat << EOF > ~/.docker/config.json\n" + string(configJSON) + "\nEOF"}
	if err := b.Runner.Run(ctx, args, nil, &buf, &buf, ""); err != nil {
		msg := fmt.Sprintf("failed to set initial docker credentials: %v\n%s", err, buf.String())
		if ctx.Err() != nil {
			log.Printf("ERROR: %v", msg)
			return ctx.Err()
		}
		return errors.New(msg)
	}
	b.prevGCRAuth = auth
	return nil
}

// UpdateDockerAccessToken updates the credentials we use to authorize requests
// to GCR.
// https://cloud.google.com/container-registry/docs/advanced-authentication#using_an_access_token
func (b *Build) UpdateDockerAccessToken(ctx context.Context, tok string) error {
	if b.prevGCRAuth == "" {
		return errors.New("UpdateDockerAccessToken called before SetDockerAccessToken")
	}

	auth := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("oauth2accesstoken:%s", tok)))

	// Update contents of ~/.docker/config.json in the homevol Docker volume.
	// Using "docker login" would work, but incurs an overhead on each host that
	// we update, because it reaches out to GCR to validate the token. Instead,
	// we can just assume the token is valid (since we just generated it) and
	// write the file directly. We need to take care not to disturb anything else
	// the user might have written to ~/.docker/config.json.

	// This sed script replaces the entire per-host config in "auths" with a new
	// per-host config with the new token, for each host.
	script := fmt.Sprintf("sed -i 's/%s/%s/g' ~/.docker/config.json", b.prevGCRAuth, auth)

	// Simply run an osImage container with $HOME mounted that runs the sed script.
	var buf bytes.Buffer
	args := []string{"docker", "run",
		"--name", fmt.Sprintf("cloudbuild_update_docker_token_%s", uuid.New()),
		"--rm",
		// Mount in the home volume.
		"--volume", homeVolume + ":" + homeDir,
		// Make /builder/home $HOME.
		"--env", "HOME=" + homeDir,
		// Make sure the container uses the correct docker daemon.
		"--volume", "/var/run/docker.sock:/var/run/docker.sock",
		"--entrypoint", "bash",
		osImage,
		"-c", script}
	if err := b.Runner.Run(ctx, args, nil, &buf, &buf, ""); err != nil {
		msg := fmt.Sprintf("failed to update docker credentials: %v\n%s", err, buf.String())
		if ctx.Err() != nil {
			log.Printf("ERROR: %v", msg)
			return ctx.Err()
		}
		return errors.New(msg)
	}
	b.prevGCRAuth = auth
	return nil
}

func (b *Build) dockerCmdOutput(ctx context.Context, cmd string, args ...string) (string, error) {
	return b.runAndScrape(ctx, append([]string{"docker", cmd}, args...))
}

func (b *Build) dockerInspect(ctx context.Context, tag string) (string, error) {
	output, err := b.dockerCmdOutput(ctx, "inspect", tag)
	if err != nil {
		return "", err
	}
	return digestForStepImage(tag, output)
}

func (b *Build) imageIsLocal(ctx context.Context, tag string) bool {
	output, err := b.dockerCmdOutput(ctx, "images", "-q", tag)
	if err != nil {
		return false
	}
	return len(output) > 0
}

func (b *Build) dockerPull(ctx context.Context, tag string, outWriter, errWriter io.Writer) (string, error) {
	// Pull from within a container with $HOME mounted.
	args := []string{"docker", "run",
		"--name", fmt.Sprintf("cloudbuild_docker_pull_%s", uuid.New()),
		"--rm",
		// Mount in the home volume.
		"--volume", homeVolume + ":" + homeDir,
		// Make /builder/home $HOME.
		"--env", "HOME=" + homeDir,
		// Make sure the container uses the correct docker daemon.
		"--volume", "/var/run/docker.sock:/var/run/docker.sock",
		"gcr.io/cloud-builders/docker",
		"pull", tag}

	var buf bytes.Buffer
	if err := b.Runner.Run(ctx, args, nil, io.MultiWriter(outWriter, &buf), errWriter, ""); err != nil {
		return "", err
	}
	return scrapePullDigest(buf.String())
}

func (b *Build) dockerPullWithRetries(ctx context.Context, tag string, outWriter, errWriter io.Writer, attempt int) (string, error) {
	digest, err := b.dockerPull(ctx, tag, outWriter, errWriter)
	if err != nil {
		if attempt < maxPushRetries {
			time.Sleep(common.Backoff(baseDelay, maxDelay, attempt))
			return b.dockerPullWithRetries(ctx, tag, outWriter, errWriter, attempt+1)
		}
		b.Log.WriteMainEntry("ERROR: failed to pull because we ran out of retries.")
		log.Print("Failed to pull because we ran out of retries.")
		return "", err
	}
	return digest, nil
}

func (b *Build) detectPushFailure(output string) error {
	const dnsFailure = "no such host"
	const networkFailure = "network is unreachable"
	const internalError = "500 Internal Server Error"
	const badGatewayError = "502 Bad Gateway"
	const tokenAuthError = "token auth attempt for registry"
	const tlsHandShakeError = "net/http: TLS handshake timeout"
	const dialTCPTimeout = "i/o timeout"
	const unknownError = "UNKNOWN"

	var status string
	defer func() {
		if status != "" {
			was := b.GCRErrors[status]
			b.GCRErrors[status] = was + 1
		}
	}()

	switch {
	case strings.Contains(output, dnsFailure):
		status = "dnsFailure"
		return errors.New(dnsFailure)
	case strings.Contains(output, networkFailure):
		status = "networkUnreachable"
		return errors.New(networkFailure)
	case strings.Contains(output, internalError):
		status = "500"
		return errors.New(internalError)
	case strings.Contains(output, badGatewayError):
		status = "502"
		return errors.New(badGatewayError)
	case strings.Contains(output, tokenAuthError):
		// We have seen the token auth error with 400s and 500s, which should
		// be retried. We will also retry with 403s, since they are the result
		// sometimes when GCS is under load.
		if status = findStatus(output); status == "" {
			status = "authError"
		}
		return errors.New(tokenAuthError)
	case strings.Contains(output, tlsHandShakeError):
		status = "tlsTimeout"
		return errors.New(tlsHandShakeError)
	case strings.Contains(output, dialTCPTimeout):
		status = "dialTCPTimeout"
		return errors.New(dialTCPTimeout)
	case strings.Contains(output, unknownError):
		status = "dockerUNKNOWN"
		return errors.New(unknownError)
	}
	status = findStatus(output)
	return nil
}

var statusRE = regexp.MustCompile(`status: (\d\d\d)`)

// findStatus pulls the http status code (if any) out of the error string
func findStatus(s string) string {
	m := statusRE.FindStringSubmatch(s)
	if len(m) == 2 {
		return m[1]
	}
	return ""
}

// Because of b/27162929, we occasionally (one out of every few hundred) get
// DNS resolution issues while pushing to GCR. Since we can detect this, we
// retry and probabilistically eliminate the issue. See also b/29115558, a GCR
// issue which seeks to address the problems we've identified.
func (b *Build) dockerPushWithRetries(ctx context.Context, tag string, attempt int) (string, error) {
	b.Log.WriteMainEntry(fmt.Sprintf("Pushing %s", tag))

	// Push from within a container with $HOME mounted.
	args := []string{"docker", "run",
		"--name", fmt.Sprintf("cloudbuild_docker_push_%s", uuid.New()),
		"--rm",
		// Mount in the home volume.
		"--volume", homeVolume + ":" + homeDir,
		// Make /builder/home $HOME.
		"--env", "HOME=" + homeDir,
		// Make sure the container uses the correct docker daemon.
		"--volume", "/var/run/docker.sock:/var/run/docker.sock",
		"gcr.io/cloud-builders/docker",
		"push", tag}

	output, err := b.runWithScrapedLogging(ctx, "PUSH", args)
	if err != nil {
		b.PushErrors++
		if derr := b.detectPushFailure(output); derr != nil {
			msg := fmt.Sprintf("push attempt %d detected failure, retrying: %v", attempt, derr)
			log.Print(msg)
			b.Log.WriteMainEntry("ERROR: " + msg)
		}
		if attempt < maxPushRetries {
			time.Sleep(common.Backoff(baseDelay, maxDelay, attempt))
			return b.dockerPushWithRetries(ctx, tag, attempt+1)
		}
		b.Log.WriteMainEntry("ERROR: failed to push because we ran out of retries.")
		log.Print("Failed to push because we ran out of retries.")
		return output, err
	}
	return output, nil
}

func stripTagDigest(nameWithTagDigest string) string {
	// Check for digest first, since only digests have "@", and both tags and digests have ":".
	if index := strings.Index(nameWithTagDigest, "@"); index != -1 {
		return nameWithTagDigest[:index]
	}
	if index := strings.Index(nameWithTagDigest, ":"); index != -1 {
		return nameWithTagDigest[:index]
	}
	return nameWithTagDigest
}

func digestForStepImage(nameWithTagDigest, output string) (string, error) {
	type imageOutput struct {
		RepoDigests []string
	}
	var repo []imageOutput
	err := json.Unmarshal([]byte(output), &repo)
	if err != nil || len(repo) < 1 || len(repo[0].RepoDigests) < 1 {
		return "", errorScrapingDigest
	}
	name := stripTagDigest(nameWithTagDigest)
	digests := repo[0].RepoDigests
	for i := 0; i < len(digests); i++ { // should only be one json output because we inspected only one image
		if strings.HasPrefix(digests[i], name) {
			rd := strings.Split(digests[i], "@")
			if len(rd) != 2 {
				return "", errorScrapingDigest
			}
			return rd[1], nil
		}
	}
	return "", errorScrapingDigest
}

func scrapePushDigests(output string) ([]imageDigest, error) {
	var digests []imageDigest // tag is just the tag, not the full image.
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		pieces := digestPushRE.FindStringSubmatch(line)
		if len(pieces) == 0 {
			continue
		}
		digests = append(digests, imageDigest{pieces[1], pieces[2]})
	}
	if len(digests) == 0 {
		return nil, errorScrapingDigest
	}
	return digests, nil
}

func scrapePullDigest(output string) (string, error) {
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		pieces := digestPullRE.FindStringSubmatch(line)
		if len(pieces) == 0 {
			continue
		}
		return pieces[1], nil
	}
	return "", errorScrapingDigest
}

func (b *Build) dockerPush(ctx context.Context, tag string) ([]imageDigest, error) {
	output, err := b.dockerPushWithRetries(ctx, tag, 1)
	if err != nil {
		return nil, err
	}
	return scrapePushDigests(output)
}

// runWithScrapedLogging executes the command and returns the output (stdin, stderr), with logging.
func (b *Build) runWithScrapedLogging(ctx context.Context, logPrefix string, cmd []string) (string, error) {
	
	var buf bytes.Buffer
	outWriter := io.MultiWriter(b.Log.MakeWriter(logPrefix+":STDOUT", -1, true), &buf)
	errWriter := io.MultiWriter(b.Log.MakeWriter(logPrefix+":STDERR", -1, false), &buf)
	err := b.Runner.Run(ctx, cmd, nil, outWriter, errWriter, "")
	return buf.String(), err
}

// runAndScrape executes the command and returns the output (stdin, stderr), without logging.
func (b *Build) runAndScrape(ctx context.Context, cmd []string) (string, error) {
	
	var buf bytes.Buffer
	outWriter := io.Writer(&buf)
	errWriter := io.Writer(&buf)
	err := b.Runner.Run(ctx, cmd, nil, outWriter, errWriter, "")
	return buf.String(), err
}

// fetchBuilder takes a step name and pulls the image if it isn't already present.
// It returns the digest of the image or empty string.
func (b *Build) fetchBuilder(ctx context.Context, idx int) (string, error) {
	name := b.Request.Steps[idx].Name

	digest, err := b.dockerInspect(ctx, name)
	outWriter := b.Log.MakeWriter(b.stepIdentifier(idx), idx, true)
	errWriter := b.Log.MakeWriter(b.stepIdentifier(idx), idx, false)
	switch err {
	case nil:
		fmt.Fprintf(outWriter, "Already have image (with digest): %s\n", name)
		return digest, nil
	case errorScrapingDigest:
		fmt.Fprintf(outWriter, "Already have image: %s\n", name)
		return "", nil
	case ctx.Err():
		return "", err
	default:
		// all other errors fall through
	}
	fmt.Fprintf(outWriter, "Pulling image: %s\n", name)
	_, err = b.dockerPullWithRetries(ctx, name, outWriter, errWriter, 0)
	switch err {
	case nil:
		// always return empty string for a pulled image, since we won't run at the
		// digest, and therefore not guaranteed to be the same image
		return "", nil
	case errorScrapingDigest:
		return "", nil
	case ctx.Err():
		return "", err
	default:
		// all other errors
		return "", fmt.Errorf("error pulling build step %d %q: %v", idx, name, err)
	}
}

// Takes step's dependencies and returns the channels that a build step must wait for.
// This method assumes the b.idxChan and b.idxTranslate is pre-populated.
func (b *Build) waitChansForStep(idx int) ([]chan struct{}, error) {
	steps := b.Request.Steps[idx].WaitFor
	switch {
	case len(steps) == 1 && steps[0] == StartStep:
		return nil, nil
	case len(steps) > 0:
		// Step requests explicit dependencies.
		var depChan []chan struct{}
		for _, dep := range steps {
			if dep != StartStep {
				idx, ok := b.idxTranslate[dep]
				if !ok {
					return nil, fmt.Errorf("build step %q translate not populated", dep)
				}
				ch, ok := b.idxChan[idx]
				if !ok {
					return nil, fmt.Errorf("build step %q channel not populated", dep)
				}
				depChan = append(depChan, ch)
			}
		}
		return depChan, nil
	default:
		// Step does not request explicit dependencies, assume it depends on all previous steps.
		var depChan []chan struct{}
		for i := 0; i < idx; i++ {
			depChan = append(depChan, b.idxChan[i])
		}
		return depChan, nil
	}
}

func (b *Build) recordStepDigest(idx int, digest string) {
	b.mu.Lock()
	b.stepDigests[idx] = digest
	b.mu.Unlock()
}

func (b *Build) GetKMSClient() (kms, error) {
	b.kmsMu.Lock()
	defer b.kmsMu.Unlock()

	if b.Kms != nil {
		return b.Kms, nil
	}

	if b.dryrun {
		b.Kms = dryRunKMS{}
		return b.Kms, nil
	}

	
	// automatically gets (and refreshes) credentials from the metadata server
	// when spoofing metadata works by IP. Until then, we'll just fetch the token
	// and pass it to all HTTP requests.
	svc, err := cloudkms.New(&http.Client{
		Transport: &common.TokenTransport{b.TokenSource},
	})
	if err != nil {
		return nil, err
	}
	b.Kms = realKMS{svc}
	return b.Kms, nil
}

// dryRunKMS always returns a base64-encoded placeholder string instead of real
// decrypted value, for use in dryrun mode.
type dryRunKMS struct{}

func (dryRunKMS) Decrypt(string, string) (string, error) {
	return base64.StdEncoding.EncodeToString([]byte("<REDACTED>")), nil
}

type realKMS struct {
	svc *cloudkms.Service
}

func (r realKMS) Decrypt(key, enc string) (string, error) {
	resp, err := r.svc.Projects.Locations.KeyRings.CryptoKeys.Decrypt(key, &cloudkms.DecryptRequest{
		Ciphertext: enc,
	}).Do()
	if err != nil {
		return "", err
	}
	return resp.Plaintext, nil
}

func (b *Build) timeAndRunStep(ctx context.Context, idx int, waitChans []chan struct{}, done chan<- struct{}, errors chan<- error) {
	// Wait for preceding steps to finish before executing.
	// If a preceding step fails, the context will cancel and waiting goroutines will die.
	
	for _, ch := range waitChans {
		select {
		case <-ch:
			continue
		case <-ctx.Done():
			return
		}
	}

	b.mu.Lock()
	b.stepStatus[idx] = pb.Build_WORKING
	var timeout time.Duration
	if stepTimeout := b.Request.Steps[idx].GetTimeout(); stepTimeout != nil {
		var err error
		timeout, err = ptypes.Duration(stepTimeout)
		// We have previously validated this stepTimeout duration, so this err should never happen.
		if err != nil {
			err = fmt.Errorf("step %d has invalid timeout %v: %v", idx, stepTimeout, err)
			log.Printf("Error: %v", err)
			errors <- err
			return
		}
	}
	start := timeNow()
	b.Timing.BuildSteps[idx] = &TimeSpan{Start: start}
	b.Timing.BuildStepPulls[idx] = &TimeSpan{Start: start}
	b.mu.Unlock()

	if b.HasMultipleSteps {
		b.Log.WriteMainEntry(fmt.Sprintf("Starting %s", b.stepIdentifier(idx)))
	}

	// Fetch the step's builder image and note how long that takes.
	digest, err := b.fetchBuilder(ctx, idx)
	end := timeNow()
	b.mu.Lock()
	b.Timing.BuildStepPulls[idx].End = end
	b.mu.Unlock()
	b.recordStepDigest(idx, digest)

	// If pulling succeeded, try running the step.
	if err == nil {
		err = b.runStep(ctx, timeout, idx, digest)
		end = timeNow()
		log.Printf("Step %s finished", b.stepIdentifier(idx))

		b.mu.Lock()
		b.Timing.BuildSteps[idx].End = end
		switch err {
		case nil:
			b.stepStatus[idx] = pb.Build_SUCCESS
		case context.DeadlineExceeded:
			// If the build step has no timeout, we got a DeadlineExceeded because the
			// overall build timed out. The step's final status is its current WORKING
			// status.
			if timeout != 0 {
				// If the build step has a timeout, then either the step timed out or the
				// build timed out (or both). If it was a build timeout, don't update the
				// per-step status.
				if stepTime := end.Sub(start); stepTime >= timeout {
					b.stepStatus[idx] = pb.Build_TIMEOUT
				}
			}
		case context.Canceled:
			b.stepStatus[idx] = pb.Build_CANCELLED
		default:
			b.stepStatus[idx] = pb.Build_FAILURE
		}
		b.mu.Unlock()
	} else {
		// Otherwise, step end time == pull end time
		b.mu.Lock()
		b.Timing.BuildSteps[idx].End = end
		b.mu.Unlock()
	}

	// If another step executing in parallel fails and sends an error, this step
	// will be blocked from sending an error on the channel.
	// Listen for context cancellation so that the goroutine exits.
	if err != nil {
		select {
		case errors <- fmt.Errorf("build step %d %q failed: %v", idx, b.Request.Steps[idx].Name, err):
		case <-ctx.Done():
		}
		return
	}
	// A step is only done if it executes successfully.
	close(done)
}

// runtimeGOOS is the operating system detected at runtime and is stubbable in testing.
var runtimeGOOS = runtime.GOOS

// osTempDir returns the default temporary directory for the OS.
func osTempDir() string {
	if runtimeGOOS == "darwin" {
		// The default temporary directory in MacOS lives in the /var path. Docker reserves the /var
		// path and will deny the build from mounting or using resources in that path. See b/78897068.
		// Use /tmp instead.
		return "/tmp"
	}
	return os.TempDir()
}

// getTempDir returns the full tempdir path. If the subpath is empty, the OS temporary directory is returned.
// Note that this does not create the temporary directory.
func getTempDir(subpath string) string {
	if subpath == "" {
		return osTempDir()
	}

	fullpath := path.Join(osTempDir(), subpath)
	if !strings.HasSuffix(fullpath, "/") {
		fullpath = fullpath + "/"
	}
	return fullpath
}

func processEnvVars(b *Build, step *pb.BuildStep) ([]string, error) {
	envVars := map[string]string{}

	// Iterate through global env vars before local in order to overwrite
	// conflicting variables
	for _, env := range append(b.Request.GetOptions().GetEnv(), step.Env...) {
		split := strings.SplitN(env, "=", 2)
		varName := split[0]
		envVars[varName] = env
	}

	secretEnvVars := append(b.Request.GetOptions().GetSecretEnv(), step.SecretEnv...)

	// If the step specifies any secrets, decrypt them and pass the plaintext
	// values as envs.
	if len(secretEnvVars) > 0 {
		kms, err := b.GetKMSClient()
		if err != nil {
			return nil, err
		}
		for _, se := range secretEnvVars {
			// Figure out which KMS key to use to decrypt the value. One and only one
			// secret should be defined for each secretEnv.
			for _, sec := range b.Request.Secrets {
				if val, found := sec.SecretEnv[se]; found {
					kmsKeyName := sec.KmsKeyName
					plaintext, err := kms.Decrypt(kmsKeyName, base64.StdEncoding.EncodeToString(val))
					if err != nil {
						return nil, fmt.Errorf("Failed to decrypt %q using key %q: %v", se, kmsKeyName, err)
					}
					dec, err := base64.StdEncoding.DecodeString(plaintext)
					if err != nil {
						return nil, fmt.Errorf("Plaintext was not base64-decodeable: %v", err)
					}
					if _, found := envVars[se]; !found {
						secret := fmt.Sprintf("%s=%s", se, string(dec))
						envVars[se] = secret
					}
					break
				}
			}
		}
	}

	keys := []string{}
	for k := range envVars {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	args := []string{}
	for _, key := range keys {
		args = append(args, "--env", envVars[key])
	}

	return args, nil
}

func (b *Build) stepIdentifier(idx int) string {
	if !b.HasMultipleSteps {
		return ""
	}
	step := b.Request.Steps[idx]
	if step.Id != "" {
		return fmt.Sprintf("Step #%d - %q", idx, step.Id)
	}
	return fmt.Sprintf("Step #%d", idx)
}

func (b *Build) runStep(ctx context.Context, timeout time.Duration, idx int, builderImageDigest string) error {
	step := b.Request.Steps[idx]

	if b.HasMultipleSteps {
		defer b.Log.WriteMainEntry(fmt.Sprintf("Finished %s", b.stepIdentifier(idx)))
	}

	runTarget := step.Name
	if builderImageDigest != "" { // only remove tag / original digest if the digest exists from the builder
		runTarget = stripTagDigest(step.Name) + "@" + builderImageDigest
	}

	var stepOutputDir string
	stepOutputDir = getTempDir(fmt.Sprintf("step-%d", idx))
	if err := b.fs.MkdirAll(stepOutputDir, os.FileMode(os.O_CREATE)); err != nil {
		return fmt.Errorf("failed to create temp dir for step outputs: %v", err)
	}

	args := b.dockerRunArgs(path.Clean(step.Dir), stepOutputDir, idx)

	envVarArgs, err := processEnvVars(b, step)
	if err != nil {
		return err
	}

	args = append(args, envVarArgs...)

	for _, vol := range append(b.Request.GetOptions().GetVolumes(), step.Volumes...) {
		args = append(args, "--volume", fmt.Sprintf("%s:%s", vol.Name, vol.Path))
	}
	if step.Entrypoint != "" {
		args = append(args, "--entrypoint", step.Entrypoint)
	}

	args = append(args, runTarget)
	args = append(args, step.Args...)

	if timeout != 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	outWriter := b.Log.MakeWriter(b.stepIdentifier(idx), idx, true)
	errWriter := b.Log.MakeWriter(b.stepIdentifier(idx), idx, false)
	buildErr := b.Runner.Run(ctx, args, nil, outWriter, errWriter, "")
	if outputErr := b.captureStepOutput(idx, stepOutputDir); outputErr != nil {
		// Output capture failed:
		// - On a failed build step, log the outputErr and return the buildErr.
		// - On a successful build step, return the outputErr (thus failing the step).
		if buildErr != nil {
			log.Printf("ERROR: step %d failed to capture outputs: %v", idx, outputErr)
			return buildErr
		}
		return outputErr
	}
	return buildErr
}

func (b *Build) captureStepOutput(idx int, stepOutputDir string) error {
	fn := path.Join(stepOutputDir, builderOutputFile)
	if exists, _ := afero.Exists(b.fs, fn); !exists {
		// Step didn't write an output file.
		return nil
	}

	b.initStepOutputsOnce.Do(func() {
		b.stepOutputs = make([][]byte, len(b.Request.Steps))
	})

	// Grab any outputs reported by the builder.
	f, err := b.fs.Open(path.Join(stepOutputDir, builderOutputFile))
	if err != nil {
		log.Printf("failed to open step %d output file: %v", idx, err)
		return err
	}

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, io.LimitReader(f, maxOutputBytes)); err != nil {
		log.Printf("failed to read step %d output file: %v", idx, err)
		return err
	}

	b.stepOutputs[idx] = buf.Bytes()
	return nil
}

func (b *Build) runBuildSteps(ctx context.Context) error {
	// Create BuildTotal TimeSpan with Start time. End time has zero value.
	b.mu.Lock()
	b.Timing.BuildTotal = &TimeSpan{Start: timeNow()}
	b.mu.Unlock()
	defer func() {
		// Populate End time in BuildTotal TimeSpan.
		// If the build is killed before the function has finished, this deferred function
		// will not execute, but we will the Start time.
		b.mu.Lock()
		b.Timing.BuildTotal.End = timeNow()
		b.mu.Unlock()
	}()

	// Create all the volumes referenced by all steps and defer cleanup.
	
	allVolumes := b.Request.GetOptions().GetVolumes()
	for _, step := range b.Request.Steps {
		allVolumes = append(allVolumes, step.GetVolumes()...)
	}

	initializedVolumes := map[string]bool{}
	for _, v := range allVolumes {
		if !initializedVolumes[v.Name] {
			initializedVolumes[v.Name] = true

			vol := volume.New(v.Name, b.Runner)
			if err := vol.Setup(ctx); err != nil {
				return err
			}
			defer func(v *volume.Volume, volName string) {
				// Clean up on a background context; main context may have been timed out or cancelled.
				ctx := context.Background()
				if err := v.Close(ctx); err != nil {
					log.Printf("Failed to delete volume %q: %v", volName, err)
				}
			}(vol, v.Name)
		}
	}

	// Clean the build steps before trying to delete the volume used by the
	// running containers. Use a background context because the main context may
	// have been timed out or cancelled.
	defer b.cleanBuildSteps(context.Background())

	b.HasMultipleSteps = len(b.Request.Steps) > 1
	errors := make(chan error)
	var finishedChannels []chan struct{}
	b.idxChan = map[int]chan struct{}{}
	b.idxTranslate = map[string]int{}
	for idx, step := range b.Request.Steps {
		if step.Id != "" {
			b.idxTranslate[step.Id] = idx
		}
	}

	b.mu.Lock()
	b.Timing.BuildSteps = make([]*TimeSpan, len(b.Request.Steps))
	b.Timing.BuildStepPulls = make([]*TimeSpan, len(b.Request.Steps))
	b.mu.Unlock()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for idx := range b.Request.Steps {
		finishedStep := make(chan struct{})
		b.idxChan[idx] = finishedStep
		waitChans, err := b.waitChansForStep(idx)
		if err != nil {
			return err
		}
		go b.timeAndRunStep(ctx, idx, waitChans, finishedStep, errors)
		finishedChannels = append(finishedChannels, finishedStep)
	}
	for _, ch := range finishedChannels {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
			continue
		case err := <-errors:
			return err
		}
	}
	// Verify that all the right images were built
	var missing []string
	for _, image := range b.Request.Images {
		if !b.imageIsLocal(ctx, image) {
			missing = append(missing, image)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("failed to find one or more images after execution of build steps: %q", missing)
	}
	return nil
}

// workdir returns the working directory for a step given the build's
// repo_source.dir and the step's dir.
func workdir(rsDir, stepDir string) string {
	if path.IsAbs(rsDir) {
		// NB: rsDir should not be absolute at this point, we've validated it
		// already.
		return ""
	}

	// If step.Dir is absolute, repoSource.Dir is ignored.
	if path.IsAbs(stepDir) {
		return path.Clean(stepDir)
	}

	return path.Clean(path.Join("/workspace", rsDir, stepDir))
}

// dockerRunArgs returns common arguments to run docker.
func (b *Build) dockerRunArgs(stepDir, stepOutputDir string, idx int) []string {
	// Take into account RepoSource dir field.
	srcDir := ""
	if dir := b.Request.GetSource().GetRepoSource().GetDir(); dir != "" {
		srcDir = dir
	}

	args := []string{"docker", "run", "--rm",
		// Gives a unique name to each build step.
		// Makes the build step easier to kill when it fails.
		"--name", fmt.Sprintf("step_%d", idx)}

		args = append(args,
			// Make sure the container uses the correct docker daemon.
			"--volume", "/var/run/docker.sock:/var/run/docker.sock",
			// Run in privileged mode.
			"--privileged")

	args = append(args,
		// Mount the project workspace.
		"--volume", b.hostWorkspaceDir+":"+containerWorkspaceDir,
		// The build step runs from the workspace dir.
		// Note: path.Join is more correct than filepath.Join. Docker volume aths
		// are always Linux forward slash paths. As this tool can run on any OS,
		// filepath.Join would produce an incorrect result.
		"--workdir", workdir(srcDir, stepDir),

		// Mount in the home volume.
		"--volume", homeVolume+":"+homeDir,
		// Make /builder/home $HOME.
		"--env", "HOME="+homeDir,

		// Connect to the network for metadata.
		"--network", "cloudbuild",

		// Mount the step output dir.
		"--volume", stepOutputDir+":"+stepOutputPath,
		// Communicate the step output path to the builder via env var.
		"--env", "BUILDER_OUTPUT="+stepOutputPath,
	)
	if !b.local {
		args = append(args,
			// Deprecated in favor of the metadata server. Mount in gsutil token cache.
			"--volume", fmt.Sprintf("%s:%s", "/root/tokencache", "/root/tokencache"),
		)
	}
	return args
}

// cleanBuildSteps kills running build steps and then removes their containers.
func (b *Build) cleanBuildSteps(ctx context.Context) {

	dockerKillArgs := []string{"docker", "rm", "-f"}
	for idx := range b.Request.Steps {
		dockerKillArgs = append(dockerKillArgs, fmt.Sprintf("step_%d", idx))
	}
	b.Runner.Run(ctx, dockerKillArgs, nil, nil, nil, "")
	if err := b.Runner.Clean(); err != nil {
		log.Printf("Failed to clean running processes: %v", err)
	}
}

// resolveDigestsForImage records all digests of images that will be pullable, by tag, as a result of a push.
func resolveDigestsForImage(image string, digests []imageDigest) []imageDigest {
	// Get the "repository" for this image (everything before the :tag).
	fields := strings.Split(image, ":")
	untaggedImage := fields[0]

	// We report all pushed tag digests, even though the customer may have only
	// asked for one.  The reason for collecting all digests is that if the
	// customer specified an untagged image, but never tagged a ":latest" (but
	// tagged a few others), then there is no single "correct" tag/digest to
	// record. Instead, we should record all of them.

	var resolvedDigests []imageDigest
	for _, d := range digests {
		tag, digest := d.tag, d.digest

		// Pulling without a tag is the same as pulling :latest, so we'll record it
		// both ways.  No matter what the user specified, the tag in this list will
		// never be empty.
		if tag == "latest" {
			resolvedDigests = append(resolvedDigests, imageDigest{untaggedImage, digest})
		}
		// If another push step already sent this image, we'll overwrite. Usually
		// the digests will be the same in this situation, but it's possible a
		// background docker task changed the tag. Either way, the digest here is
		// something that was actually pushed so we'll record it.
		resolvedDigests = append(resolvedDigests, imageDigest{fmt.Sprintf("%s:%s", untaggedImage, tag), digest})
	}
	return resolvedDigests
}

// push will push images to GCR and non-container artifacts to GCS.
func (b *Build) pushAll(ctx context.Context) error {
	// Don't record a PushTotal time if nothing is being pushed.
	if b.Request.Images == nil && (b.Request.Artifacts == nil || b.Request.Artifacts.Objects == nil) {
		return nil
	}

	b.mu.Lock()
	b.Timing.PushTotal = &TimeSpan{Start: timeNow()}
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		b.Timing.PushTotal.End = timeNow()
		b.mu.Unlock()
	}()

	if err := b.pushImages(ctx); err != nil {
		return err
	}
	return b.pushArtifacts(ctx)
}

func (b *Build) pushImages(ctx context.Context) error {
	if b.Request.Images == nil {
		return nil
	}

	b.mu.Lock()
	b.Timing.ImagePushes = make(map[string]*TimeSpan)
	b.mu.Unlock()
	for _, image := range b.Request.Images {
		start := timeNow()
		digests, err := b.dockerPush(ctx, image)
		timing := &TimeSpan{Start: start, End: timeNow()}

		switch err {
		case nil:
			// Do nothing.
		case errorScrapingDigest:
			log.Println("Unable to find a digest")
			continue
		case ctx.Err():
			return err
		default:
			// all other errors
			return fmt.Errorf("error pushing image %q: %v", image, err)
		}
		if len(digests) != 0 {
			log.Printf("Found %d digests", len(digests))
		}
		resolvedDigests := resolveDigestsForImage(image, digests)
		b.mu.Lock()
		for _, d := range resolvedDigests {
			b.imageDigests = append(b.imageDigests, d)
			// In cases where the same image is pushed multiple times, only store the timing for the first push.
			// When you try to push an image that already exists, the timing is negligible.
			if _, ok := b.Timing.ImagePushes[d.digest]; !ok {
				b.Timing.ImagePushes[d.digest] = timing
			}
		}
		b.mu.Unlock()
	}
	return nil
}

// GCS URL to bucket
func extractGCSBucket(url string) string {
	toks := strings.SplitN(strings.TrimPrefix(url, "gs://"), "/", 2)
	return fmt.Sprintf("gs://%s", toks[0])
}

var newUUID = uuid.New

// pushArtifacts pushes ArtifactObjects to a specified bucket.
func (b *Build) pushArtifacts(ctx context.Context) error {
	if b.Request.Artifacts == nil || b.Request.Artifacts.Objects == nil {
		return nil
	}

	// Only verify that the GCS bucket exists.
	// If they specify a directory path in the bucket that doesn't exist, gsutil will create it for them.
	
	location := b.Request.Artifacts.Objects.Location
	bucket := extractGCSBucket(location)
	if err := b.gsutilHelper.VerifyBucket(ctx, bucket); err != nil {
		return err
	}

	// Upload specified artifacts from the workspace to the GCS location.
	workdir := containerWorkspaceDir
	if dir := b.Request.GetSource().GetRepoSource().GetDir(); dir != "" {
		workdir = path.Join(workdir, dir)
	}
	flags := gsutil.DockerFlags{
		Workvol: b.hostWorkspaceDir + ":" + containerWorkspaceDir,
		Workdir: workdir,
		Tmpdir:  osTempDir(),
	}

	b.mu.Lock()
	b.Timing.ArtifactsPushes = &TimeSpan{Start: timeNow()}
	b.mu.Unlock()

	b.Log.WriteMainEntry(fmt.Sprintf("Artifacts will be uploaded to %s using gsutil cp", bucket))
	results := []*pb.ArtifactResult{}
	for _, src := range b.Request.Artifacts.Objects.Paths {
		b.Log.WriteMainEntry(fmt.Sprintf("%s: Uploading path....", src))
		r, err := b.gsutilHelper.UploadArtifacts(ctx, flags, src, location)
		if err != nil {
			return fmt.Errorf("could not upload %s to %s; err = %v", src, location, err)
		}

		results = append(results, r...)
		b.Log.WriteMainEntry(fmt.Sprintf("%s: %d matching files uploaded", src, len(r)))
	}
	numArtifacts := int64(len(results))
	b.Log.WriteMainEntry(fmt.Sprintf("%d total artifacts uploaded to %s", numArtifacts, location))

	b.mu.Lock()
	b.Timing.ArtifactsPushes.End = timeNow()
	b.mu.Unlock()

	// Write a JSON manifest for the artifacts and upload to the GCS location.
	filename := fmt.Sprintf("artifacts-%s.json", b.Request.Id)
	b.Log.WriteMainEntry(fmt.Sprintf("Uploading manifest %s", filename))
	artifactManifest, err := b.gsutilHelper.UploadArtifactsManifest(ctx, flags, filename, location, results)
	if err != nil {
		return fmt.Errorf("could not upload %s to %s; err = %v", filename, location, err)
	}
	b.Log.WriteMainEntry(fmt.Sprintf("Artifact manifest located at %s", artifactManifest))

	// Store uploaded artifact information to be returned in build results.
	b.artifacts = ArtifactsInfo{ArtifactManifest: artifactManifest, NumArtifacts: numArtifacts}

	return nil
}
