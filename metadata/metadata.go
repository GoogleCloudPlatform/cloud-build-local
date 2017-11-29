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
//
// In order to imitate the GCE environment to provide credentials and some
// other project metadata, we run a metadata service container and make it
// available to build steps as metadata.google.internal, metadata, and
// the fixed IP 169.254.169.254.
//
// The GCE metadata service is documented here:
//  https://cloud.google.com/compute/docs/storing-retrieving-metadata
// The imitation metadata service we run offers a subset of the true
// metadata functionality, focused on providing credentials to client
// libraries.
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
	"regexp"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/GoogleCloudPlatform/container-builder-local/runner"
)

const (
	// fixedMetadataIP is the fixed IP available on all GCE instances that serves
	// metadata on port 80. Some client libraries connect directly to this IP,
	// rather than metadata.google.internal, so we need to listen on this IP.
	fixedMetadataIP = "169.254.169.254"
	// This subnet captures the fixed metadata IP. This subnet is a reserved link-local subnet.
	metadataLocalSubnet = "169.254.0.0/16"

	// metadataHostedIP is the IP that the metadata spoofer listens to in the
	// hosted cloudbuild environment. iptables is used to route tcp connections going to
	// 169.254.169.254 port 80 to this IP instead.
	metadataHostedIP = "192.168.10.5"
	// This subnet captures metadataHostedIP. This subnet is a reserved private subnet.
	// This is the subnet used to create the cloudbuild docker network in the hosted
	// container builder environment. All build steps are run connected to the
	// cloudbuild docker network.
	metadataHostedSubnet = "192.168.10.0/24"
)

// Updater encapsulates updating the spoofed metadata server.
type Updater interface {
	SetToken(*Token) error
	SetProjectInfo(ProjectInfo) error
}

// ProjectInfo represents an incoming build request containing the project ID
// and number to make available as metadata.
type ProjectInfo struct {
	ProjectID  string `json:"project_id"`
	ProjectNum int64  `json:"project_num"`
}

// Token represents an OAuth token including the access token, account email,
// expiration, and scopes.
type Token struct {
	AccessToken string    `json:"access_token"`
	Expiry      time.Time `json:"expiry"`
	Email       string    `json:"email"`
	Scopes      []string
}

// Oauth2 converts a Token to a standard oauth2.Token.
func (t Token) Oauth2() *oauth2.Token {
	return &oauth2.Token{
		AccessToken: t.AccessToken,
		Expiry:      t.Expiry,
		TokenType:   "Bearer",
	}
}

// RealUpdater actually sends POST requests to update spoofed metadata.
type RealUpdater struct {
	Local bool
}

// findIP extracts an ip address from a string using a regex pattern.
func findIP(input string) string {
	if input == "" {
		return input
	}
	numBlock := "(25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])"
	regexPattern := numBlock + "\\." + numBlock + "\\." + numBlock + "\\." + numBlock

	regEx := regexp.MustCompile(regexPattern)
	return regEx.FindString(input)
}

func (r RealUpdater) getAddress() string {
	if !r.Local {
		return metadataHostedIP
	}
	// If DOCKER_HOST not set (the usual case when r.Local is true)
	// then use localhost:8082 for the address of the spoofed metadata server.
	dh := os.Getenv("DOCKER_HOST")
	if dh == "" {
		return "localhost:8082"
	}

	// If DOCKER_HOST is set, then the user may be using minikube locally.
	// This feature was added to support the minikube use case.

	// If an ip cannot be extracted from DOCKER_HOST using the findIP regex function
	// then use localhost:8082 for the address of the spoofed metadata server and log a warning.
	dh = findIP(dh)
	if dh == "" {
		log.Println("Warning DOCKER_HOST environment variable set but does not contain a valid ip address, falling back to using localhost:8082 for spoofed metadata server address")
		return "localhost:8082"
	}

	// The spoofed metadata server will be created at the docker host address defined by DOCKER_HOST,
	// so need to return the docker host address instead of localhost.
	return dh + ":8082"
}

// SetToken updates the spoofed metadata server's credentials.
func (r RealUpdater) SetToken(tok *Token) error {
	scopes, err := getScopes(tok.AccessToken)
	if err != nil {
		return err
	}
	tok.Scopes = scopes
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(tok); err != nil {
		return err
	}

	if resp, err := http.Post("http://"+r.getAddress()+"/token", "application/json", &buf); err != nil {
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

// SetProjectInfo updates the spoofed metadata server's project information.
func (r RealUpdater) SetProjectInfo(b ProjectInfo) error {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(b); err != nil {
		return err
	}
	if resp, err := http.Post("http://"+r.getAddress()+"/build", "application/json", &buf); err != nil {
		return err
	} else if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		all, _ := ioutil.ReadAll(resp.Body)
		return fmt.Errorf("POST failed (%d): %s", resp.StatusCode, string(all))
	}
	return nil
}

// StartLocalServer starts the metadata server container for VMs running as
// part of the container builder service.
//
// This version of Start*Server does not update iptables.
//
// The container listens on local port 8082, which is where RealUpdater POSTs
// to.
func StartLocalServer(r runner.Runner, metadataImage string) error {
	// Unlike the hosted container builder service, the user's local machine is
	// not guaranteed to have the latest version, so we explicitly pull it.
	if err := r.Run([]string{"docker", "pull", metadataImage}, nil, os.Stdout, os.Stderr, ""); err != nil {
		return err
	}
	return startServer(r, metadataImage, false, fixedMetadataIP, metadataLocalSubnet)
}

// StartCloudServer starts the metadata server container for VMs running as
// part of the container builder service.
//
// This version of Start*Server needs to make iptables rules that we don't
// want (or need) on a user's local machine.
//
// The container listens on local port 8082, which is where RealUpdater POSTs
// to.
func StartCloudServer(r runner.Runner, metadataImage string) error {
	if err := startServer(r, metadataImage, true, metadataHostedIP, metadataHostedSubnet); err != nil {
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

// CreateCloudbuildNetwork creates a cloud build network to link the build
// builds.
func CreateCloudbuildNetwork(r runner.Runner, subnet string) error {
	cmd := []string{"docker", "network", "create", "cloudbuild", "--subnet=" + subnet}
	return r.Run(cmd, nil, nil, os.Stderr, "")
}

func startServer(r runner.Runner, metadataImage string, iptables bool, ip, subnet string) error {
	if err := CreateCloudbuildNetwork(r, subnet); err != nil {
		return fmt.Errorf("Error creating network: %v", err)
	}

	// Run the spoofed metadata server.
	var cmd []string
	if !iptables {
		// In the local builder, we need to expose the port but it's nice to avoid 80.
		cmd = []string{"docker", "run", "-d", "-p=8082:80", "--name=metadata", metadataImage}
	} else {
		cmd = []string{"docker", "run", "-d", "--name=metadata", metadataImage}
	}
	if err := r.Run(cmd, nil, nil, os.Stderr, ""); err != nil {
		return err
	}

	// Redirect requests to metadata.google.internal and the fixed metadata IP to the metadata container.
	cmd = []string{"docker", "network", "connect", "--alias=metadata", "--alias=metadata.google.internal", "--ip=" + ip, "cloudbuild", "metadata"}
	if err := r.Run(cmd, nil, nil, os.Stderr, ""); err != nil {
		return fmt.Errorf("Error connecting metadata to network: %v", err)
	}

	if iptables {
		// Route the "real" metadata IP to the IP of our metadata spoofer.
		cmd = []string{"iptables",
			"-t", "nat", // In the nat table,
			"-A", "PREROUTING", // append this rule to the PREROUTING chain,
			"-p", "tcp", // for connections using TCP,
			"--destination", fixedMetadataIP, // intended for the metadata service,
			"--dport", "80", // intended for port 80.
			"-j", "DNAT", // This rule does destination NATting,
			"--to-destination", metadataHostedIP, // to our spoofed metadata container.
		}
		if err := r.Run(cmd, nil, os.Stdout, os.Stderr, ""); err != nil {
			return fmt.Errorf("Error updating iptables: %v", err)
		}
	}

	return nil
}

// CleanCloudbuildNetwork delete the cloudbuild network.
func CleanCloudbuildNetwork(r runner.Runner) error {
	return r.Run([]string{"docker", "network", "rm", "cloudbuild"}, nil, nil, os.Stderr, "")
}

// Stop stops the metadata server container and tears down the docker cloudbuild
// network used to route traffic to it.
// Try to clean both the container and the network before returning an error.
func (RealUpdater) Stop(r runner.Runner) error {
	errContainer := r.Run([]string{"docker", "rm", "-f", "metadata"}, nil, nil, os.Stderr, "")
	errNetwork := CleanCloudbuildNetwork(r)
	if errContainer != nil {
		return errContainer
	}
	return errNetwork
}
