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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/cloud-build-local/metadata"
	"github.com/GoogleCloudPlatform/cloud-build-local/runner"
)

var (
	errTokenNotFound = errors.New("failed to find access token")
	errAcctNotFound  = errors.New("failed to find credentials account")
	errTokenExpired  = errors.New("gcloud token is expired")
)

// AccessToken gets a fresh access token from gcloud.
func AccessToken(ctx context.Context, r runner.Runner) (*metadata.Token, error) {
	// config struct matches the json output of the cmd below.
	var config struct {
		Credential struct {
			AccessToken string `json:"access_token"`
			TokenExpiry string `json:"token_expiry"`
		}
		Configuration struct {
			Properties struct {
				Core struct {
					Account string
				}
			}
		}
	}

	cmd := []string{"gcloud", "config", "config-helper", "--format=json"}
	var b bytes.Buffer
	if err := r.Run(ctx, cmd, nil, &b, os.Stderr, ""); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b.Bytes(), &config); err != nil {
		return nil, err
	}
	if config.Credential.AccessToken == "" {
		return nil, errTokenNotFound
	}
	if config.Configuration.Properties.Core.Account == "" {
		return nil, errAcctNotFound
	}
	expiration, err := time.Parse(time.RFC3339, config.Credential.TokenExpiry)
	if err != nil {
		return nil, err
	}
	if expiration.Before(time.Now()) {
		return nil, errTokenExpired
	}
	return &metadata.Token{
		AccessToken: config.Credential.AccessToken,
		Expiry:      expiration,
		Email:       config.Configuration.Properties.Core.Account,
	}, nil
}

// ProjectInfo gets the project id and number from local gcloud.
func ProjectInfo(ctx context.Context, r runner.Runner) (metadata.ProjectInfo, error) {
	cmd := []string{"gcloud", "config", "list", "--format", "value(core.project)"}
	var idb, numb bytes.Buffer
	if err := r.Run(ctx, cmd, nil, &idb, os.Stderr, ""); err != nil {
		return metadata.ProjectInfo{}, err
	}
	projectID := strings.TrimSpace(idb.String())

	if projectID == "" {
		return metadata.ProjectInfo{}, fmt.Errorf("no project is set in gcloud, use 'gcloud config set project my-project'")
	}

	cmd = []string{"gcloud", "projects", "describe", projectID, "--format", "value(projectNumber)"}
	if err := r.Run(ctx, cmd, nil, &numb, os.Stderr, ""); err != nil {
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
