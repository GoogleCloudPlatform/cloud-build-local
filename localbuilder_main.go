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
	"path/filepath"
	"strings"
	"time"

	computeMetadata "cloud.google.com/go/compute/metadata"
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
	volumeNamePrefix  = "cloudbuild_vol_"
	tokenRefreshDur   = 10 * time.Minute
	gcbDockerVersion  = "17.05-ce"
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
	source := args[0]
	if *configFile == "" {
		exitUsage("Specify a config file")
	}

	if err := run(source); err != nil {
		log.Fatal(err)
	}
}

// run method is used to encapsulate the local builder process, being
// able to return errors to the main function which will then panic. So the
// run function can probably run all the defer functions in any case.
func run(source string) error {
	// Create a runner.
	r := &runner.RealRunner{
		DryRun: *dryRun,
	}

	// Clean leftovers from a previous build.
	if err := common.Clean(r); err != nil {
		return fmt.Errorf("Error cleaning: %v", err)
	}

	// Check installed docker versions.
	if !*dryRun {
		dockerServerVersion, dockerClientVersion, err := dockerVersions(r)
		if err != nil {
			return fmt.Errorf("Error getting local docker versions: %v", err)
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
		return fmt.Errorf("Error loading config file: %v", err)
	}

	// Parse substitutions.
	if *substitutions != "" {
		substMap, err := common.ParseSubstitutionsFlag(*substitutions)
		if err != nil {
			return fmt.Errorf("Error parsing substitutions flag: %v", err)
		}
		buildConfig.Substitutions = substMap
	}

	// Validate the build.
	if err := validate.CheckBuild(buildConfig); err != nil {
		return fmt.Errorf("Error validating build: %v", err)
	}

	// Apply substitutions.
	if err := subst.SubstituteBuildFields(buildConfig); err != nil {
		return fmt.Errorf("Error applying substitutions: %v", err)
	}

	// Create a volume, a helper container to copy the source, and defer cleaning.
	volumeName := fmt.Sprintf("%s%s", volumeNamePrefix, uuid.New())
	if !*dryRun {
		vol := volume.New(volumeName, r)
		if err := vol.Setup(); err != nil {
			return fmt.Errorf("Error creating docker volume: %v", err)
		}
		// If the source is a directory, only copy the inner content.
		if isDir, err := isDirectory(source); err != nil {
			return fmt.Errorf("Error getting directory: %v", err)
		} else if isDir {
			source = filepath.Clean(source) + "/."
		}
		if err := vol.Copy(source); err != nil {
			return fmt.Errorf("Error copying source to docker volume: %v", err)
		}
		defer vol.Close()
	}

	b := build.New(r, *buildConfig, nil, &buildlog.BuildLog{}, volumeName, true, *push)

	// Do not run the spoofed metadata server on a dryrun.
	if !*dryRun {
		// On GCE, do not create a spoofed metadata server, use the existing one.
		// The cloudbuild network is still needed, with a private subnet.
		if computeMetadata.OnGCE() {
			if err := metadata.CreateCloudbuildNetwork(r, "172.22.0.0/16"); err != nil {
				return fmt.Errorf("Error creating network: %v", err)
			}
			defer metadata.CleanCloudbuildNetwork(r)
		} else {
			if err := metadata.StartLocalServer(r, metadataImageName); err != nil {
				return fmt.Errorf("Failed to start spoofed metadata server: %v", err)
			}
			log.Println("Started spoofed metadata server")
			metadataUpdater := metadata.RealUpdater{Local: true}
			defer metadataUpdater.Stop(r)

			// Get project info to feed the metadata server.
			projectInfo, err := gcloud.ProjectInfo(r)
			if err != nil {
				return fmt.Errorf("Error getting project information from gcloud: %v", err)
			}
			metadataUpdater.SetProjectInfo(projectInfo)

			go supplyTokenToMetadata(metadataUpdater, r)
		}

		// Set initial Docker credentials.
		tok, err := gcloud.AccessToken(r)
		if err != nil {
			return fmt.Errorf("Error getting access token to set docker credentials: %v", err)
		}
		if err := b.SetDockerAccessToken(tok); err != nil {
			return fmt.Errorf("Error setting docker credentials: %v", err)
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
	}

	b.Start()
	<-b.Done

	if b.Summary().Status == build.StatusError {
		return fmt.Errorf("Build finished with ERROR status")
	}

	if *dryRun {
		log.Printf("Warning: this was a dry run; add --dryrun=false if you want to run the build locally.")
	}
	return nil
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

func isDirectory(path string) (bool, error) {
	fileInfo, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	mode := fileInfo.Mode()
	return mode.IsDir(), nil
}
