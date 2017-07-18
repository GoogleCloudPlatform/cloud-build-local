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

// Package main runs the gcb local builder.
package main // import "github.com/GoogleCloudPlatform/container-builder-local"

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"github.com/google/uuid"

	"github.com/GoogleCloudPlatform/container-builder-local/build"
	"github.com/GoogleCloudPlatform/container-builder-local/buildlog"
	"github.com/GoogleCloudPlatform/container-builder-local/common"
	"github.com/GoogleCloudPlatform/container-builder-local/config"
	"github.com/GoogleCloudPlatform/container-builder-local/gcloud"
	"github.com/GoogleCloudPlatform/container-builder-local/metadata"
	"github.com/GoogleCloudPlatform/container-builder-local/runner"
	"github.com/GoogleCloudPlatform/container-builder-local/subst"
	"github.com/GoogleCloudPlatform/container-builder-local/validate"
	"github.com/GoogleCloudPlatform/container-builder-local/volume"
)

const (
	volumeNamePrefix  = "gcb-local-vol-"
	tokenRefreshDur   = 10 * time.Minute
	gcbDockerVersion  = "17.05"
	metadataImageName = "gcr.io/cloud-builders/metadata"
)

var (
	configFile    = flag.String("config", "cloudconfig.yaml", "cloud build config file path")
	substitutions = flag.String("substitutions", "", `substitutions key=value pairs separated by comma; for example _FOO=bar,_BAZ=argo`)
	dryRun        = flag.Bool("dryrun", true, "If true, nothing will be run")
	push          = flag.Bool("push", false, "If true, the images will be pushed")
	help          = flag.Bool("help", false, "If true, print the help message")
	versionFlag   = flag.Bool("version", false, "If true, print the local builder version")
)

func exitUsage(msg string) {
	log.Fatalf("%s\nUsage: %s --config=cloudbuild.yaml [--substitutions=_FOO=bar] [--[no]dryrun] [--[no]push] source", msg, os.Args[0])
}

func main() {
	flag.Parse()
	args := flag.Args()

	if *help {
		flag.PrintDefaults()
		return
	}
	if *versionFlag {
		log.Printf("Version: %s", version)
		return
	}

	if len(args) == 0 {
		exitUsage("Specify a source")
	} else if len(args) > 1 {
		exitUsage("There should be only one positional argument. Pass all the flags before the source.")
	}
	dir := args[0]
	if *configFile == "" {
		exitUsage("Specify a config file")
	}

	// Create a runner.
	r := &runner.RealRunner{
		DryRun: *dryRun,
	}

	// Check installed docker versions.
	if !*dryRun {
		dockerServerVersion, dockerClientVersion, err := dockerVersions(r)
		if err != nil {
			log.Printf("Error getting local docker versions: %v", err)
			return
		}
		if dockerServerVersion != gcbDockerVersion {
			log.Printf("Warning: The server docker version installed (%s) is different from the one used in GCB (%s)", dockerServerVersion, gcbDockerVersion)
		}
		if dockerClientVersion != gcbDockerVersion {
			log.Printf("Warning: The client docker version installed (%s) is different from the one used in GCB (%s)", dockerClientVersion, gcbDockerVersion)
		}
	}

	// Load config file into a build struct.
	buildConfig, err := config.Load(*configFile)
	if err != nil {
		log.Printf("Error loading config file: %v", err)
		return
	}

	// Parse substitutions.
	if *substitutions != "" {
		substMap, err := common.ParseSubstitutionsFlag(*substitutions)
		if err != nil {
			log.Printf("Error parsing substitutions flag: %v", err)
			return
		}
		buildConfig.Substitutions = substMap
	}

	// Validate the build.
	if err := validate.CheckBuild(buildConfig); err != nil {
		log.Printf("Error validating build: %v", err)
		return
	}

	// Apply substitutions.
	if err := subst.SubstituteBuildFields(buildConfig); err != nil {
		log.Printf("Error applying substitutions: %v", err)
		return
	}

	// Create a volume, a helper container to copy the source, and defer cleaning.
	volumeName := fmt.Sprintf("%s%s", volumeNamePrefix, uuid.New())
	if !*dryRun {
		vol := volume.New(volumeName, r)
		if err := vol.Setup(); err != nil {
			log.Printf("Error creating docker volume: %v", err)
			return
		}
		if err := vol.Copy(dir); err != nil {
			log.Printf("Error copying directory to docker volume: %v", err)
			return
		}
		defer vol.Close()
	}

	b := build.New(r, *buildConfig, nil, &buildlog.BuildLog{}, volumeName, true, *push)

	if !*dryRun {
		// Start the spoofed metadata server.
		log.Println("Starting spoofed metadata server...")
		if err := metadata.StartLocalServer(r, metadataImageName); err != nil {
			log.Printf("Failed to start spoofed metadata server: %v", err)
			return
		}
		log.Println("Started spoofed metadata server")
		metadataUpdater := metadata.RealUpdater{Local: true}
		defer metadataUpdater.Stop(r)

		// Get project info to feed the metadata server.
		projectInfo, err := gcloud.ProjectInfo(r)
		if err != nil {
			log.Printf("Error getting project information from gcloud: %v", err)
			return
		}
		metadataUpdater.SetProjectInfo(projectInfo)

		// Set initial Docker credentials.
		tok, err := gcloud.AccessToken(r)
		if err != nil {
			log.Printf("Error getting access token to set docker credentials: %v", err)
			return
		}
		if err := b.SetDockerAccessToken(tok); err != nil {
			log.Printf("Error setting docker credentials: %v", err)
			return
		}

		// Write initial docker credentials for GCR. This writes the initial
		// ~/.docker/config.json which is made available to build steps.
		
		go func() {
			for ; ; time.Sleep(tokenRefreshDur) {
				tok, err := gcloud.AccessToken(r)
				if err != nil {
					log.Printf("Error getting access token to update docker credentials: %v", err)
					continue
				}
				if err := b.UpdateDockerAccessToken(tok); err != nil {
					log.Printf("Error updating docker credentials: %v", err)
				}
			}
		}()

		go supplyTokenToMetadata(metadataUpdater, r)

	}

	b.Start()

	if *dryRun {
		log.Printf("Warning: this was a dry run; add --dryrun=false if you want to run the build locally.")
	}
}

// supplyTokenToMetadata gets gcloud token and supply it to the metadata server.
func supplyTokenToMetadata(metadataUpdater metadata.RealUpdater, r runner.Runner) {
	for ; ; time.Sleep(tokenRefreshDur) {
		accessToken, err := gcloud.AccessToken(r)
		if err != nil {
			log.Printf("Error getting gcloud token: %v", err)
			continue
		}
		token := oauth2.Token{
			AccessToken: accessToken,
			
			// https://developers.google.com/identity/sign-in/web/backend-auth#calling-the-tokeninfo-endpoint
			Expiry: time.Now().Add(2 * tokenRefreshDur),
		}
		if err := metadataUpdater.SetToken(token); err != nil {
			log.Printf("Error updating token in metadata server: %v", err)
			continue
		}
	}
}

// dockerVersion gets local server and client docker versions.
func dockerVersions(r runner.Runner) (string, string, error) {
	cmd := []string{"docker", "version", "--format", "{{.Server.Version}}"}
	var serverb bytes.Buffer
	if err := r.Run(cmd, nil, &serverb, os.Stderr, ""); err != nil {
		return "", "", err
	}

	cmd = []string{"docker", "version", "--format", "{{.Client.Version}}"}
	var clientb bytes.Buffer
	if err := r.Run(cmd, nil, &clientb, os.Stderr, ""); err != nil {
		return "", "", err
	}

	return strings.TrimSpace(serverb.String()), strings.TrimSpace(clientb.String()), nil
}
