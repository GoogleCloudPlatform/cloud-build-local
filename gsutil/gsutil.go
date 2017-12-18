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

// Package gsutil provides helper functions for running gsutil commands with Docker.
package gsutil

import (
	"fmt"
	"io/ioutil"

	"github.com/GoogleCloudPlatform/container-builder-local/runner"
	"github.com/google/uuid"
)

// newUUID returns a new uuid string and is stubbed during testing.
var newUUID = uuid.New

// New returns a new Helper struct.
func New(r runner.Runner) Helper {
	return Helper{runner: r}
}

// Helper provides helper functions for running gsutil commands in docker.
type Helper struct {
	runner runner.Runner
}

// BucketExists takes in the name of a GCS bucket and returns whether or not it exists.
func (g Helper) BucketExists(bucket string) bool {
	dockerArgs := []string{"docker", "run",
		// Assign container name.
		"--name", fmt.Sprintf("cloudbuild_gsutil_ls_%s", newUUID()),
		// Remove the container when it exits.
		"--rm",
		// Make sure the container uses the correct docker daemon.
		"--volume", "/var/run/docker.sock:/var/run/docker.sock",
		// Connect to the network for metadata to get credentials.
		"--network", "cloudbuild",
		"gcr.io/cloud-builders/gsutil", "ls", bucket}

	err := g.runner.Run(dockerArgs, nil, ioutil.Discard, ioutil.Discard, "")

	// Running 'ls' on a bucket that doesn't exist or has an invalid GCS URL will return an error.
	return err == nil
}
