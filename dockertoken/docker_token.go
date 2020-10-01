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

// Package dockertoken provides methods to interact with docker access token.
package dockertoken

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/GoogleCloudPlatform/cloud-build-local/build"
	"github.com/GoogleCloudPlatform/cloud-build-local/runner"
)

const (
	dockerTokenContainerName = "docker_token_container"

	// osImage identifies an image (1) containing a base OS image that we can use
	// to manipulate files on a Docker volume that (2) we know is either already
	// locally available or is going to be (or very likely to be) pulled anyway
	// for use during the build. We use our docker container image for this
	// purpose.
	osImage = "busybox"

	// shell used, it should be compatible with the osImage.
	shell = "ash"
)

// Updater encapsulates updating the docker token access.
type Updater interface {
	Set(context.Context, string) error
	Close(context.Context) error
}

// DockerToken is responsible for managing the docker token access.
type DockerToken struct {
	runner runner.Runner
}

// New creates a new instance of DockerToken and creates the container.
func New(ctx context.Context, r runner.Runner) (*DockerToken, error) {
	dt := &DockerToken{
		runner: r,
	}
	if err := dt.createContainer(ctx); err != nil {
		return nil, err
	}
	return dt, nil
}

// createContainer creates the running container that will be used
// to write the token to the home volume.
func (d *DockerToken) createContainer(ctx context.Context) error {
	var buf bytes.Buffer
	args := []string{"docker", "run",
		"--name", dockerTokenContainerName,
		// Mount in the home volume.
		"--volume", build.HomeVolume + ":" + build.HomeDir,
		// Make /builder/home $HOME.
		"--env", "HOME=" + build.HomeDir,
		// Make sure the container uses the correct docker daemon.
		"--volume", "/var/run/docker.sock:/var/run/docker.sock",
		"-itd",
		osImage,
		shell}
	if err := d.runner.Run(ctx, args, nil, &buf, &buf); err != nil {
		msg := fmt.Sprintf("failed to create docker token container: %v\n%s", err, buf.String())
		if ctx.Err() != nil {
			log.Printf("ERROR: %v", msg)
			return ctx.Err()
		}
		return errors.New(msg)
	}
	return nil
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
	// Artifact Registry regional endpoints
	"https://asia-docker.pkg.dev",
	"https://asia-east1-docker.pkg.dev",
	"https://asia-east2-docker.pkg.dev",
	"https://asia-northeast1-docker.pkg.dev",
	"https://asia-northeast2-docker.pkg.dev",
	"https://asia-south1-docker.pkg.dev",
	"https://asia-southeast1-docker.pkg.dev",
	"https://australia-southeast1-docker.pkg.dev",
	"https://europe-docker.pkg.dev",
	"https://europe-north1-docker.pkg.dev",
	"https://europe-west1-docker.pkg.dev",
	"https://europe-west2-docker.pkg.dev",
	"https://europe-west3-docker.pkg.dev",
	"https://europe-west4-docker.pkg.dev",
	"https://europe-west6-docker.pkg.dev",
	"https://gcr-staging.sandbox.google.com",
	"https://northamerica-northeast1-docker.pkg.dev",
	"https://southamerica-east1-docker.pkg.dev",
	"https://us-central1-docker.pkg.dev",
	"https://us-docker.pkg.dev",
	"https://us-east1-docker.pkg.dev",
	"https://us-east4-docker.pkg.dev",
	"https://us-west1-docker.pkg.dev",
	"https://us-west2-docker.pkg.dev",
}

// Set sets Docker config with the credentials we use to authorize requests to
// GCR.
// https://cloud.google.com/container-registry/docs/advanced-authentication#using_an_access_token
func (d *DockerToken) Set(ctx context.Context, tok string) error {
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

	script := "mkdir -p ~/.docker/ && cat << EOF > ~/.docker/config.json\n" + string(configJSON) + "\nEOF"

	// Simply run an osImage container with $HOME mounted that writes the ~/.docker/config.json file.
	var buf bytes.Buffer
	args := []string{"docker", "exec", dockerTokenContainerName, shell, "-c", script}
	if err := d.runner.Run(ctx, args, nil, &buf, &buf); err != nil {
		msg := fmt.Sprintf("failed to set initial docker credentials: %v\n%s", err, buf.String())
		if ctx.Err() != nil {
			log.Printf("ERROR: %v", msg)
			return ctx.Err()
		}
		return errors.New(msg)
	}
	return nil
}

// Close deletes the running container.
func (d *DockerToken) Close(ctx context.Context) error {
	var buf bytes.Buffer
	if err := d.runner.Run(ctx, []string{"docker", "rm", "-f", dockerTokenContainerName}, nil, &buf, &buf); err != nil {
		msg := fmt.Sprintf("failed to delete docker token container: %v\n%s", err, buf.String())
		if ctx.Err() != nil {
			log.Printf("ERROR: %v", msg)
			return ctx.Err()
		}
		return errors.New(msg)
	}
	return nil
}
