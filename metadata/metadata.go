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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/GoogleCloudPlatform/cloud-build-local/runner"
)

const (
	// fixedMetadataIP is the fixed IP available on all GCE instances that serves
	// metadata on port 80. Some client libraries connect directly to this IP,
	// rather than metadata.google.internal, so we need to listen on this IP.
	fixedMetadataIP = "169.254.169.254"
	// This subnet captures the fixed metadata IP. This subnet is a reserved link-local subnet.
	metadataLocalSubnet = "169.254.0.0/16"

	// This subnet captures metadataHostedIP. This subnet is a reserved private subnet.
	// This is the subnet used to create the cloudbuild docker network in the
	// hosted Cloud Build environment. All build steps are run connected to the
	// cloudbuild docker network.
	metadataHostedSubnet = "192.168.10.0/24"

	// localMetadata is the host:port for metadata when running the local builder.
	localMetadata = "http://localhost:8082"

	// Header constants
	headerKey   = "Metadata-Flavor"
	headerVal   = "Google"
	contentType = "Content-Type"
)

var (
	// metadataHostedIP is the IP that the metadata spoofer listens to in the
	// hosted cloudbuild environment. iptables is used to route tcp connections
	// going to 169.254.169.254 port 80 to this IP instead.
	metadataHostedIP = "192.168.10.5" // var for testing

	// httpTimeout is the timeout for http calls; var for testing
	httpTimeout = 10 * time.Second

	jsonHeader = http.Header{contentType: []string{"application/json"}}
)

// Updater encapsulates updating the spoofed metadata server.
type Updater interface {
	SetToken(context.Context, *Token) error
	SetProjectInfo(context.Context, ProjectInfo) error
	Ready(context.Context) bool // Returns true if metadata is up and running.
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

func (r RealUpdater) getAddress() string {
	addr := localMetadata
	if !r.Local {
		addr = metadataHostedIP
	}
	if !strings.HasPrefix(addr, "http") {
		addr = "http://" + addr
	}
	return addr
}

func doRequest(ctx context.Context, method, url string, body io.Reader, header http.Header) (io.ReadCloser, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header = header
	req.Header.Set(headerKey, headerVal)

	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		all, _ := ioutil.ReadAll(resp.Body)
		return nil, fmt.Errorf("%s failed (%d): %s", method, resp.StatusCode, string(all))
	}
	return resp.Body, nil
}

// SetToken updates the spoofed metadata server's credentials.
func (r RealUpdater) SetToken(ctx context.Context, tok *Token) error {
	scopes, err := getScopes(ctx, tok.AccessToken)
	if err != nil {
		return err
	}
	tok.Scopes = scopes
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(tok); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, httpTimeout)
	defer cancel()
	body, err := doRequest(ctx, http.MethodPost, r.getAddress()+"/token", &buf, jsonHeader)
	if err != nil {
		return err
	}
	body.Close()
	return nil
}

// var for testing
var googleTokenInfoHost = "https://www.googleapis.com"

// getScopes returns the OAuth scopes provided by the access token.
//
// It determines this by POSTing to the tokeninfo API:
// https://developers.google.com/identity/protocols/OAuth2UserAgent#validatetoken
func getScopes(ctx context.Context, tok string) ([]string, error) {
	data := url.Values{}
	data.Set("access_token", tok)

	const maxTries = 3
	var err error
	for i := 0; i < maxTries; i++ {
		if i > 0 {
			delay := time.Millisecond * 150 * time.Duration(i)
			log.Printf("Waiting %v before retry...", delay)
			time.Sleep(delay)
		}

		var body io.ReadCloser
		ctx, cancel := context.WithTimeout(ctx, httpTimeout)
		defer cancel()
		body, err = doRequest(ctx, http.MethodPost, googleTokenInfoHost+"/oauth2/v3/tokeninfo",
			strings.NewReader(data.Encode()), http.Header{contentType: []string{"application/x-www-form-urlencoded"}})
		if err != nil {
			log.Printf("Error reading scopes on attempt #%d/%d: %v", i+1, maxTries, err)
			continue
		}
		defer body.Close()
		r := struct {
			Scope string `json:"scope"`
		}{}
		if err = json.NewDecoder(body).Decode(&r); err != nil {
			log.Printf("Error decoding scopes response on attempt #%d/%d: %v", i+1, maxTries, err)
			continue
		}
		return strings.Split(r.Scope, " "), nil
	}
	return nil, err
}

// SetProjectInfo updates the spoofed metadata server's project information.
func (r RealUpdater) SetProjectInfo(ctx context.Context, b ProjectInfo) error {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(b); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, httpTimeout)
	defer cancel()
	body, err := doRequest(ctx, http.MethodPost, r.getAddress()+"/build", &buf, jsonHeader)
	if err != nil {
		return err
	}

	defer body.Close()
	return nil
}

// Ready returns true if the metadata server is up and running.
func (r RealUpdater) Ready(ctx context.Context) bool {
	body, err := doRequest(ctx, http.MethodGet, r.getAddress()+"/ready", nil, http.Header{})
	if err != nil {
		log.Printf("Metadata not ready: %v", err)
		return false
	}
	body.Close()
	return true
}

// StartLocalServer starts the metadata server container for VMs running
// independent from the Cloud Build service.
//
// This version of Start*Server does not update iptables.
//
// The container listens on local port 8082, which is where RealUpdater POSTs
// to.
func StartLocalServer(ctx context.Context, r runner.Runner, metadataImage string) error {
	// Unlike the hosted Cloud Build service, the user's local machine is
	// not guaranteed to have the latest version, so we explicitly pull it.
	if err := r.Run(ctx, []string{"docker", "pull", metadataImage}, nil, os.Stdout, os.Stderr, ""); err != nil {
		return err
	}
	return startServer(ctx, r, metadataImage, false, fixedMetadataIP, metadataLocalSubnet)
}

// StartCloudServer starts the metadata server container for VMs running as
// part of the Cloud Build service.
//
// This version of Start*Server needs to make iptables rules that we don't
// want (or need) on a user's local machine.
//
// The container listens on local port 8082, which is where RealUpdater POSTs
// to.
func StartCloudServer(ctx context.Context, r runner.Runner, metadataImage string) error {
	if err := startServer(ctx, r, metadataImage, true, metadataHostedIP, metadataHostedSubnet); err != nil {
		return err
	}

	// In a separate goroutine, attach to the metadata server container so its
	// logs are properly captured and available for debugging.
	go func() {
		if err := r.Run(ctx, []string{"docker", "attach", "metadata"}, nil, os.Stdout, os.Stderr, ""); err != nil {
			log.Printf("docker attach failed: %v", err)
		}
	}()

	return nil
}

// CreateCloudbuildNetwork creates a cloud build network to link the build
// builds.
func CreateCloudbuildNetwork(ctx context.Context, r runner.Runner, subnet string) error {
	cmd := []string{"docker", "network", "create", "cloudbuild", "--subnet=" + subnet}
	return r.Run(ctx, cmd, nil, nil, os.Stderr, "")
}

func startServer(ctx context.Context, r runner.Runner, metadataImage string, iptables bool, ip, subnet string) error {
	if err := CreateCloudbuildNetwork(ctx, r, subnet); err != nil {
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
	if err := r.Run(ctx, cmd, nil, nil, os.Stderr, ""); err != nil {
		return err
	}

	// Redirect requests to metadata.google.internal and the fixed metadata IP to the metadata container.
	cmd = []string{"docker", "network", "connect", "--alias=metadata", "--alias=metadata.google.internal", "--ip=" + ip, "cloudbuild", "metadata"}
	if err := r.Run(ctx, cmd, nil, nil, os.Stderr, ""); err != nil {
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
		if err := r.Run(ctx, cmd, nil, os.Stdout, os.Stderr, ""); err != nil {
			return fmt.Errorf("Error updating iptables: %v", err)
		}
	}

	return nil
}

// CleanCloudbuildNetwork delete the cloudbuild network.
func CleanCloudbuildNetwork(ctx context.Context, r runner.Runner) error {
	return r.Run(ctx, []string{"docker", "network", "rm", "cloudbuild"}, nil, nil, os.Stderr, "")
}

// Stop stops the metadata server container and tears down the docker cloudbuild
// network used to route traffic to it.
// Try to clean both the container and the network before returning an error.
func (RealUpdater) Stop(ctx context.Context, r runner.Runner) error {
	errContainer := r.Run(ctx, []string{"docker", "rm", "-f", "metadata"}, nil, nil, os.Stderr, "")
	errNetwork := CleanCloudbuildNetwork(ctx, r)
	if errContainer != nil {
		return errContainer
	}
	return errNetwork
}
