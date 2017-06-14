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

// Package metadata provides methods to deal with a metadata container server.
package metadata

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/container-builder-local/runner"

	"golang.org/x/oauth2"
)

// Updater encapsulates updating the spoofed metadata server.
type Updater interface {
	SetToken(oauth2.Token) error
	SetProjectInfo(ProjectInfo) error
}

// ProjectInfo represents an incoming build request containing the project ID
// and number to make available as metadata.
type ProjectInfo struct {
	ProjectID  string `json:"project_id"`
	ProjectNum int64  `json:"project_num"`
}

// Token represents the OAuth token request containing the access token and the
// time it expires.
type Token struct {
	AccessToken string    `json:"access_token"`
	Expiry      time.Time `json:"expiry"`
	Scopes      []string
}

// RealUpdater actually sends POST requests to update spoofed metadata.
type RealUpdater struct{}

// SetToken updates the spoofed metadata server's credentials.
func (RealUpdater) SetToken(tok oauth2.Token) error {
	scopes, err := getScopes(tok.AccessToken)
	if err != nil {
		return err
	}
	t := Token{
		AccessToken: tok.AccessToken,
		Expiry:      tok.Expiry,
		Scopes:      scopes,
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(t); err != nil {
		return err
	}
	if resp, err := http.Post("http://localhost:8082/token", "application/json", &buf); err != nil {
		return err
	} else if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Got HTTP %d from spoofed metadata", resp.StatusCode)
	}
	return nil
}

// var for testing
var googleTokenInfoHost = "https://www.googleapis.com"

// getScopes returns the OAuth scopes provided by the access token.
//
// It determines this by POSTing to the tokeninfo API:
// https://developers.google.com/identity/protocols/OAuth2UserAgent#validatetoken
func getScopes(tok string) ([]string, error) {
	data := url.Values{}
	data.Set("access_token", tok)
	resp, err := http.Post(googleTokenInfoHost+"/oauth2/v3/tokeninfo", "application/x-www-form-urlencoded", strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		all, _ := ioutil.ReadAll(resp.Body)
		return nil, fmt.Errorf("POST failed (%d): %s", resp.StatusCode, string(all))
	}
	r := struct {
		Scope string `json:"scope"`
	}{}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	resp.Body.Close()
	return strings.Split(r.Scope, " "), nil
}

// SetBuild updates the spoofed metadata server's project information.
func (RealUpdater) SetProjectInfo(b ProjectInfo) error {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(b); err != nil {
		return err
	}
	if resp, err := http.Post("http://localhost:8082/build", "application/json", &buf); err != nil {
		return err
	} else if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		all, _ := ioutil.ReadAll(resp.Body)
		return fmt.Errorf("POST failed (%d): %s", resp.StatusCode, string(all))
	}
	return nil
}

// StartServer starts the metadata server container.
//
// The container is mapped to hostPort 8082, which is where RealUpdater POSTs
// to.
func StartServer(r runner.Runner, metadataImage string) error {
	// Run the spoofed metadata server, mapped to localhost:8082
	cmd := []string{"docker", "run", "-d", "-p=8082:80", "--name=metadata", metadataImage}
	log.Println(cmd)
	if err := r.Run(cmd, nil, os.Stdout, os.Stderr, ""); err != nil {
		return err
	}

	// In a separate goroutine, attach to the metadata server container so its
	// logs get printed to the worker's logs and ferried up to the foreman after
	// the build is done.
	go func() {
		if err := r.Run([]string{"docker", "attach", "metadata"}, nil, os.Stdout, os.Stderr, ""); err != nil {
			log.Printf("docker attach failed: %v", err)
		}
	}()

	return nil
}

// Stop stops the metadata server container.
func (RealUpdater) Stop(r runner.Runner) error {
	cmd := []string{"docker", "rm", "-f", "metadata"}
	return r.Run(cmd, nil, os.Stdout, os.Stderr, "")
}
