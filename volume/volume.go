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

// Package volume provides methods to deal with docker volumes.
package volume

import (
	"fmt"

	"github.com/GoogleCloudPlatform/container-builder-local/runner/runner"
)

const (
	workspaceDir = "/workspace"
)

// Volume is responsible for managing the docker volume.
type Volume struct {
	name   string
	helper string
	runner runner.Runner
}

// New creates a new Volume.
func New(name string, r runner.Runner) *Volume {
	return &Volume{
		name:   name,
		helper: name + "-helper",
		runner: r,
	}
}

// Setup creates a docker volume and a helper container that can be used to
// copy data to the volume.
func (v *Volume) Setup() error {
	if err := v.create(); err != nil {
		return err
	}
	return v.createHelperContainer()
}

func (v *Volume) create() error {
	cmd := []string{"docker", "volume", "create", "--name", v.name}
	return v.runner.Run(cmd, nil, nil, nil, "")
}

func (v *Volume) createHelperContainer() error {
	volume := fmt.Sprintf("%s:%s", v.name, workspaceDir)
	cmd := []string{"docker", "run", "-v", volume, "--name", v.helper, "busybox"}
	return v.runner.Run(cmd, nil, nil, nil, "")
}

// Copy copies files from a directory dir to the docker volume.
func (v *Volume) Copy(dir string) error {
	helperVol := fmt.Sprintf("%s:%s", v.helper, workspaceDir)
	cmd := []string{"docker", "cp", dir, helperVol}
	return v.runner.Run(cmd, nil, nil, nil, "")
}

// Close cleans up the helper container and the docker volume.
func (v *Volume) Close() error {
	if err := v.deleteHelper(); err != nil {
		return err
	}
	return v.deleteVolume()
}

func (v *Volume) deleteHelper() error {
	cmd := []string{"docker", "rm", v.helper}
	return v.runner.Run(cmd, nil, nil, nil, "")
}

func (v *Volume) deleteVolume() error {
	cmd := []string{"docker", "volume", "rm", v.name}
	return v.runner.Run(cmd, nil, nil, nil, "")
}
