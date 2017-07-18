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

// Package gcloud provides methods to interact with local installed gcloud.
package gcloud

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/GoogleCloudPlatform/container-builder-local/metadata"
	"github.com/GoogleCloudPlatform/container-builder-local/runner"
)

// AccessToken gets a fresh access token from gcloud.
func AccessToken(r runner.Runner) (string, error) {
	cmd := []string{"gcloud", "config", "config-helper", "--format=value(credential.access_token)"}
	var tb bytes.Buffer
	if err := r.Run(cmd, nil, &tb, os.Stderr, ""); err != nil {
		return "", err
	}
	return strings.TrimSpace(tb.String()), nil
}

// ProjectInfo gets the project id and number from local gcloud.
func ProjectInfo(r runner.Runner) (metadata.ProjectInfo, error) {

	cmd := []string{"gcloud", "config", "list", "--format", "value(core.project)"}
	var idb, numb bytes.Buffer
	if err := r.Run(cmd, nil, &idb, os.Stderr, ""); err != nil {
		return metadata.ProjectInfo{}, err
	}
	projectID := strings.TrimSpace(idb.String())

	if projectID == "" {
		return metadata.ProjectInfo{}, fmt.Errorf("no project is set in gcloud, use 'gcloud config set project my-project'")
	}

	cmd = []string{"gcloud", "projects", "describe", projectID, "--format", "value(projectNumber)"}
	if err := r.Run(cmd, nil, &numb, os.Stderr, ""); err != nil {
		return metadata.ProjectInfo{}, err
	}
	projectNum, err := strconv.ParseInt(strings.TrimSpace(numb.String()), 10, 64)
	if err != nil {
		return metadata.ProjectInfo{}, err
	}

	return metadata.ProjectInfo{
		ProjectID:  projectID,
		ProjectNum: projectNum,
	}, nil
}
