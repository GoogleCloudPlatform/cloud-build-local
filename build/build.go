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
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	cb "google.golang.org/genproto/googleapis/devtools/cloudbuild/v1"

	"github.com/GoogleCloudPlatform/container-builder-local/buildlog"
	"github.com/GoogleCloudPlatform/container-builder-local/common"
	"github.com/GoogleCloudPlatform/container-builder-local/runner"
	"github.com/GoogleCloudPlatform/container-builder-local/volume"
	"google.golang.org/api/cloudkms/v1"
	"golang.org/x/oauth2"
	"github.com/google/uuid"
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
)

var (
	errorScrapingDigest = errors.New("no digest in output")
	// digestPushRE is a regular expression that parses the digest out of the
	// 'docker push' output. For example,
	// "1450274640_0: digest: sha256:e7e2025236b06b1d8978bffce0cb545613d02f6b7f1959ca6f8d121c93ea3103 size: 9914"
	digestPushRE = regexp.MustCompile(`^(.+):\s*digest:\s*(sha256:[^\s]+)\s*size:\s*\d+$`)
	// digestPullRE is a regular expression that parses the digest out of the
	// 'docker pull' output. For example,
	// "Digest: sha256:070e421b4d5f88e0dd16b1214a47696ab18a7fa2d1f14fbf9ff52799c91df05c"
	digestPullRE = regexp.MustCompile(`^Digest:\s*(sha256:[^\s]+)$`)
	// RunRm : if true, all `docker run` commands will be passed a `--rm` flag.
	RunRm = false
)

// Build manages a single build.
type Build struct {
	// Lock to update the status of a build or read/write digests.
	Mu               sync.Mutex
	Request          cb.Build
	HasMultipleSteps bool
	Tokensource      oauth2.TokenSource
	Log              *buildlog.BuildLog
	Status           BuildStatus
	imageDigests     map[string]string // docker image tag to digest (for built images)
	stepDigests      []string          // build step index to digest (for build steps)
	err              error
	Runner           runner.Runner
	Done             chan struct{}
	Times            map[BuildStatus]time.Duration
	LastStateStart   time.Time
	idxChan          map[int]chan struct{}
	idxTranslate     map[string]int
	kmsMu            sync.Mutex // guards accesses to kms
	kms              kms

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

	// For UpdateDockerAccessToken, previous GCR auth value to replace.
	prevGCRAuth string
}

type kms interface {
	Decrypt(key, enc string) (string, error)
}

// New constructs a new Build.
func New(r runner.Runner, rq cb.Build, ts oauth2.TokenSource,
	bl *buildlog.BuildLog, hostWorkspaceDir string, local, push bool) *Build {
	return &Build{
		Runner:           r,
		Request:          rq,
		Tokensource:      ts,
		imageDigests:     map[string]string{},
		stepDigests:      make([]string, len(rq.Steps)),
		Log:              bl,
		Done:             make(chan struct{}),
		Times:            map[BuildStatus]time.Duration{},
		LastStateStart:   time.Now(),
		GCRErrors:        map[string]int64{},
		hostWorkspaceDir: hostWorkspaceDir,
		local:            local,
		push:             push,
	}
}

// Start executes a single build.
func (b *Build) Start() {
	if b.local {
		defer func() {
			// The worker will close it after it finishes all its work, which includes
			// more steps (source fetching, etc...).
			// Wait for all logging to finish.
			if err := b.Log.Close(); err != nil {
				b.Status = StatusError
			}
			close(b.Done)
		}()
		if err := b.Log.SetupPrint(); err != nil {
			b.Failbuild(err)
		}
	}

	// Create the home volume.
	homeVol := volume.New(homeVolume, b.Runner)
	if err := homeVol.Setup(); err != nil {
		b.Failbuild(err)
		return
	}
	defer func() {
		if err := homeVol.Close(); err != nil {
			log.Printf("Failed to delete homevol: %v", err)
		}
	}()

	// Fetch and run the build steps.
	b.UpdateStatus(StatusBuild)
	if err := b.runBuildSteps(); err != nil {
		b.Failbuild(err)
		return
	}

	// push the images
	if b.push {
		b.UpdateStatus(StatusPush)
		if err := b.pushImages(); err != nil {
			b.Failbuild(err)
			return
		}
	}

	// build is done
	b.UpdateStatus(StatusDone)
}

// GetStatus returns the build's status in a thread-safe way.
func (b *Build) GetStatus() BuildStatus {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	return b.Status
}

// Summary returns the build's summary in a thread-safe way.
func (b *Build) Summary() BuildSummary {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	s := BuildSummary{
		Status: b.Status, // don't use .GetStatus() - that would double-lock.
	}
	tags := []string{}
	for tag := range b.imageDigests {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	for _, tag := range tags {
		digest := b.imageDigests[tag]
		s.BuiltImages = append(s.BuiltImages, BuiltImage{
			Name:   tag,
			Digest: digest,
		})
	}

	for _, digest := range b.stepDigests {
		s.BuildStepImages = append(s.BuildStepImages, digest)
	}

	return s
}

// UpdateStatus updates the current status.
func (b *Build) UpdateStatus(status BuildStatus) {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	b.Times[b.Status] = time.Since(b.LastStateStart)

	b.LastStateStart = time.Now()

	log.Printf("status changed to %q", string(status))
	b.Log.WriteMainEntry(string(status))
	b.Status = status

}

// Failbuild updates the build's status to failure.
func (b *Build) Failbuild(err error) {
	b.UpdateStatus(StatusError)
	b.Log.WriteMainEntry("ERROR: " + err.Error())
	b.Mu.Lock()
	b.err = err
	b.Mu.Unlock()
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
}

// SetDockerAccessToken sets the initial Docker config with the credentials we
// use to authorize requests to GCR.
// https://cloud.google.com/container-registry/docs/advanced-authentication#using_an_access_token
func (b *Build) SetDockerAccessToken(tok string) error {
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

	// Simply run an "ubuntu" image with $HOME mounted that writes the ~/.docker/config.json file.
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
		"ubuntu",
		"-c", "mkdir -p ~/.docker/ && cat << EOF > ~/.docker/config.json\n" + string(configJSON) + "\nEOF"}
	if err := b.Runner.Run(args, nil, &buf, &buf, ""); err != nil {
		return fmt.Errorf("failed to set initial docker credentials: %v\n%s", err, buf.String())
	}
	b.prevGCRAuth = auth
	return nil
}

// UpdateDockerAccessToken updates the credentials we use to authorize requests
// to GCR.
// https://cloud.google.com/container-registry/docs/advanced-authentication#using_an_access_token
func (b *Build) UpdateDockerAccessToken(tok string) error {
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

	// Simply run an "ubuntu" image with $HOME mounted that runs the sed script.
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
		"ubuntu",
		"-c", script}
	if err := b.Runner.Run(args, nil, &buf, &buf, ""); err != nil {
		return fmt.Errorf("failed to update docker credentials: %v\n%s", err, buf.String())
	}
	b.prevGCRAuth = auth
	return nil
}

func (b *Build) dockerCmdOutput(cmd string, args ...string) (string, error) {
	dockerArgs := append([]string{"docker", cmd}, args...)
	return b.runAndScrape(dockerArgs, "")
}

func (b *Build) dockerInspect(tag string) (string, error) {
	output, err := b.dockerCmdOutput("inspect", tag)
	if err != nil {
		return "", err
	}
	return digestForStepImage(tag, output)
}

func (b *Build) imageIsLocal(tag string) bool {
	_, err := b.dockerInspect(tag)
	return err == nil || err == errorScrapingDigest // if the error was scraping the digest, the image is indeed local
}

func (b *Build) dockerPull(tag string, outWriter, errWriter io.Writer) (string, error) {
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
	if err := b.Runner.Run(args, nil, io.MultiWriter(outWriter, &buf), errWriter, ""); err != nil {
		return "", err
	}
	return scrapePullDigest(buf.String())
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
// DNS resolution issues while pushing to GCR. Since we can detect them, we can
// retry and probabilistically eliminate the issue. See also b/29115558, a
// Convoy issue which seeks to address the problems we've identified.
func (b *Build) dockerPushWithRetries(tag string, attempt int) (string, error) {
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

	output, err := b.runWithScrapedLogging("PUSH", args, "")
	if err != nil {
		b.PushErrors++
		if derr := b.detectPushFailure(output); derr != nil {
			msg := fmt.Sprintf("push attempt %d detected failure, retrying: %v", attempt, derr)
			log.Print(msg)
			b.Log.WriteMainEntry("ERROR: " + msg)
		}
		if attempt < maxPushRetries {
			time.Sleep(common.Backoff(500*time.Millisecond, 10*time.Second, attempt))
			return b.dockerPushWithRetries(tag, attempt+1)
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

func scrapePushDigests(output string) (map[string]string, error) {
	digests := map[string]string{}
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		pieces := digestPushRE.FindStringSubmatch(line)
		if len(pieces) == 0 {
			continue
		}
		digests[pieces[1]] = pieces[2]
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

func (b *Build) dockerPush(tag string) (map[string]string, error) {
	output, err := b.dockerPushWithRetries(tag, 1)
	if err != nil {
		return nil, err
	}
	return scrapePushDigests(output)
}

// runWithScrapedLogging executes the command and returns the output (stdin, stderr), with logging.
func (b *Build) runWithScrapedLogging(logPrefix string, cmd []string, dir string) (string, error) {
	var buf bytes.Buffer
	outWriter := io.MultiWriter(b.Log.MakeWriter(logPrefix+":STDOUT"), &buf)
	errWriter := io.MultiWriter(b.Log.MakeWriter(logPrefix+":STDERR"), &buf)
	err := b.Runner.Run(cmd, nil, outWriter, errWriter, dir)
	return buf.String(), err
}

// runAndScrape executes the command and returns the output (stdin, stderr), without logging.
func (b *Build) runAndScrape(cmd []string, dir string) (string, error) {
	var buf bytes.Buffer
	outWriter := io.Writer(&buf)
	errWriter := io.Writer(&buf)
	err := b.Runner.Run(cmd, nil, outWriter, errWriter, dir)
	return buf.String(), err
}

// fetchBuilder takes a step name and pulls the image if it isn't already present.
// It returns the digest of the image or empty string.
func (b *Build) fetchBuilder(name string, stepIdentifier string) (string, error) {
	digest, err := b.dockerInspect(name)
	outWriter := b.Log.MakeWriter(fmt.Sprintf("%s", stepIdentifier))
	errWriter := b.Log.MakeWriter(fmt.Sprintf("%s", stepIdentifier))
	if err == errorScrapingDigest {
		fmt.Fprintf(outWriter, "Already have image: %s\n", name)
		return "", nil
	}
	if err == nil {
		fmt.Fprintf(outWriter, "Already have image (with digest): %s\n", name)
		return digest, nil
	}
	fmt.Fprintf(outWriter, "Pulling image: %s\n", name)
	_, err = b.dockerPull(name, outWriter, errWriter)
	if err == errorScrapingDigest {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("error pulling build step %q: %v", name, err)
	}
	// always return empty string for a pulled image, since we won't run at the digest, and therefore not guaranteed to be the same image
	return "", nil
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
	b.Mu.Lock()
	b.stepDigests[idx] = digest
	b.Mu.Unlock()
}

func (b *Build) getKMSClient() (kms, error) {
	b.kmsMu.Lock()
	defer b.kmsMu.Unlock()

	if b.kms != nil {
		return b.kms, nil
	}

	
	// automatically gets (and refreshes) credentials from the metadata server
	// when spoofing metadata works by IP. Until then, we'll just fetch the token
	// and pass it to all HTTP requests.
	svc, err := cloudkms.New(&http.Client{
		Transport: &tokenTransport{b.Tokensource},
	})
	if err != nil {
		return nil, err
	}
	b.kms = realKMS{svc}
	return b.kms, nil
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

func (b *Build) fetchAndRunStep(step *cb.BuildStep, idx int, waitChans []chan struct{}, done chan<- struct{}, errors chan error) {
	for _, ch := range waitChans {
		<-ch
	}
	var stepIdentifier string
	if b.HasMultipleSteps {
		if step.Id != "" {
			stepIdentifier = fmt.Sprintf("Step #%d - %q", idx, step.Id)
		} else {
			stepIdentifier = fmt.Sprintf("Step #%d", idx)
		}
	}
	outWriter := b.Log.MakeWriter(fmt.Sprintf("%s", stepIdentifier))
	errWriter := b.Log.MakeWriter(fmt.Sprintf("%s", stepIdentifier))

	digest, err := b.fetchBuilder(step.Name, stepIdentifier)
	if err != nil {
		errors <- err
		return
	}
	b.recordStepDigest(idx, digest)

	if b.HasMultipleSteps {
		b.Log.WriteMainEntry(fmt.Sprintf("Starting %s", stepIdentifier))
	}

	runTarget := step.Name
	if digest != "" { // only remove tag / original digest if the digest exists from the builder
		runTarget = stripTagDigest(step.Name) + "@" + digest
	}

	args := b.dockerRunArgs(path.Clean(step.Dir), idx)
	for _, env := range step.Env {
		args = append(args, "--env", env)
	}


	if step.Entrypoint != "" {
		args = append(args, "--entrypoint", step.Entrypoint)
	}


	args = append(args, runTarget)
	args = append(args, step.Args...)
	if err := b.Runner.Run(args, nil, outWriter, errWriter, ""); err != nil {
		errors <- fmt.Errorf("build step %q failed: %v", runTarget, err)
	} else {
		defer close(done)
	}
	if b.HasMultipleSteps {
		b.Log.WriteMainEntry(fmt.Sprintf("Finished %s", stepIdentifier))
	}
}

func (b *Build) runBuildSteps() error {

	// Clean the build steps before trying to delete the volume used by the
	// running containers.
	defer b.cleanBuildSteps()

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
	for idx, step := range b.Request.Steps {
		finishedStep := make(chan struct{})
		b.idxChan[idx] = finishedStep
		waitChans, err := b.waitChansForStep(idx)
		if err != nil {
			return err
		}
		go b.fetchAndRunStep(step, idx, waitChans, finishedStep, errors)
		finishedChannels = append(finishedChannels, finishedStep)
	}
	for _, ch := range finishedChannels {
		select {
		case <-ch:
			continue
		case err := <-errors:
			return err
		}
	}
	// verify that all the right images were built
	var missing []string
	for _, image := range b.Request.Images {
		if !b.imageIsLocal(image) {
			missing = append(missing, image)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("failed to find one or more images after execution of build steps: %q", missing)
	}
	return nil
}

// dockerRunArgs returns common arguments to run docker.
func (b *Build) dockerRunArgs(stepDir string, idx int) []string {
	args := []string{"docker", "run"}
	if RunRm {
		// remove the container when it exits
		args = append(args, "--rm")
	}

	args = append(args,
		// Gives a unique name to each build step.
		// Makes the build step easier to kill when it fails.
		"--name", fmt.Sprintf("step_%d", idx),
		// Make sure the container uses the correct docker daemon.
		"--volume", "/var/run/docker.sock:/var/run/docker.sock",
		// Mount the project workspace.
		"--volume", b.hostWorkspaceDir+":"+containerWorkspaceDir,
		// The build step runs from the workspace dir.
		// Note: path.Join is more correct than filepath.Join. Docker volume aths
		// are always Linux forward slash paths. As this tool can run on any OS,
		// filepath.Join would produce an incorrect result.
		"--workdir", path.Join(containerWorkspaceDir, stepDir),
		// Mount in the home volume.
		"--volume", homeVolume+":"+homeDir,
		// Make /builder/home $HOME.
		"--env", "HOME="+homeDir,
		// Connect to the network for metadata.
		"--network", "cloudbuild",
		// Run in privileged mode per discussion in b/31267381.
		"--privileged",
	)
	if !b.local {
		args = append(args,
			// Deprecated in favor of the metadata server. Mount in gsutil token cache.
			"--volume", fmt.Sprintf("%s:%s", "/root/tokencache", "/root/tokencache"),
		)
	}
	return args
}

// cleanBuildSteps first kill and remove build step containers.
func (b *Build) cleanBuildSteps() {
	dockerKillArgs := []string{"docker", "rm", "-f"}
	for idx := range b.Request.Steps {
		dockerKillArgs = append(dockerKillArgs, fmt.Sprintf("step_%d", idx))
	}
	_ = b.Runner.Run(dockerKillArgs, nil, nil, nil, "")
	if err := b.Runner.Clean(); err != nil {
		log.Printf("Failed to clean running processes: %v", err)
	}
}

// resolveDigestsForImage records all digests of images that will be pullable, by tag, as a result of a push.
func (b *Build) resolveDigestsForImage(image string, digests map[string]string) map[string]string {
	// Get the "repository" for this image (everything before the :tag).
	fields := strings.Split(image, ":")
	untaggedImage := fields[0]

	// We report all pushed tag digests, even though the customer may have only asked for one.
	// The reason for collecting all digests is that if the customer specified an untagged
	// image, but never tagged a ":latest" (but tagged a few others), then there is no single
	// "correct" tag/digest to record. Instead, we should record all of them.

	resolvedDigests := map[string]string{}
	for tag, digest := range digests {
		// Pulling without a tag is the same as pulling :latest, so we'll record it both ways.
		// No matter what the user specified, the tag in this list will never be empty.
		if tag == "latest" {
			resolvedDigests[untaggedImage] = digest
		}
		// If another push step already sent this image, we'll overwrite. Usually the digests
		// will be the same in this situation, but it's possible a background docker task
		// changed the tag. Either way, the digest here is soemthing that was actually pushed
		// so we'll record it.
		resolvedDigests[fmt.Sprintf("%s:%s", untaggedImage, tag)] = digest
	}
	return resolvedDigests
}

func (b *Build) pushImages() error {
	if b.Request.Images == nil {
		return nil
	}
	for _, image := range b.Request.Images {
		digests, err := b.dockerPush(image)
		if err == errorScrapingDigest {
			log.Println("Unable to find a digest")
			continue
		}
		if err != nil {
			return fmt.Errorf("error pushing image %q: %v", image, err)
		}
		if len(digests) != 0 {
			log.Printf("Found %d digests", len(digests))
		}
		resolvedDigests := b.resolveDigestsForImage(image, digests)
		b.Mu.Lock()
		for i, d := range resolvedDigests {
			b.imageDigests[i] = d
		}
		b.Mu.Unlock()
	}
	return nil
}

// tokenTransport is a RoundTripper that automatically applies OAuth
// credentials from the token source.
//
// This can be replaced by google.DefaultClient when metadata spoofing works by
// IP address (b/33233310).
type tokenTransport struct {
	ts oauth2.TokenSource
}

func (t *tokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tok, err := t.ts.Token()
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	return http.DefaultTransport.RoundTrip(req)
}
