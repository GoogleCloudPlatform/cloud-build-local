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
	"bufio"
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/afero"
	computeMetadata "cloud.google.com/go/compute/metadata"
	"golang.org/x/oauth2"
	"github.com/pborman/uuid"

	"github.com/GoogleCloudPlatform/container-builder-local/build"
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
	gcbDockerVersion  = "17.12.0-ce"
	metadataImageName = "gcr.io/cloud-builders/metadata"
)

var (
	configFile     = flag.String("config", "cloudbuild.yaml", "File path of the config file")
	substitutions  = flag.String("substitutions", "", `key=value pairs where the key is already defined in the build request; separate multiple substitutions with a comma, for example: _FOO=bar,_BAZ=baz`)
	dryRun         = flag.Bool("dryrun", true, "Lints the config file and prints but does not run the commands; Local Builder runs the commands only when dryrun is set to false")
	push           = flag.Bool("push", false, "Pushes the images to the registry")
	noSource       = flag.Bool("no-source", false, "Prevents Local Builder from using source for this build")
	writeWorkspace = flag.String("write-workspace", "", "Copies the workspace directory to this host directory")
	help           = flag.Bool("help", false, "Prints the help message")
	versionFlag    = flag.Bool("version", false, "Prints the local builder version")
)

func exitUsage(msg string) {
	log.Fatalf("%s\nUsage: %s --config=cloudbuild.yaml [--substitutions=_FOO=bar] [--dryrun=true/false] [--push=true/false] source", msg, os.Args[0])
}

func main() {
	flag.Parse()
	ctx := context.Background()
	args := flag.Args()

	if *help {
		flag.PrintDefaults()
		return
	}
	if *versionFlag {
		log.Printf("Version: %s", version)
		return
	}

	nbSource := 1
	if *noSource {
		nbSource = 0
	}

	if len(args) < nbSource {
		exitUsage("Specify a source")
	} else if len(args) > nbSource {
		if nbSource == 1 {
			exitUsage("There should be only one positional argument. Pass all the flags before the source.")
		} else {
			exitUsage("no-source flag can't be used along with source.")
		}
	}
	source := ""
	if nbSource == 1 {
		source = args[0]
	}

	if *configFile == "" {
		exitUsage("Specify a config file")
	}

	log.SetOutput(os.Stdout)
	if err := run(ctx, source); err != nil {
		log.Fatal(err)
	}
}

// run method is used to encapsulate the local builder process, being
// able to return errors to the main function which will then panic. So the
// run function can probably run all the defer functions in any case.
func run(ctx context.Context, source string) error {
	// Create a runner.
	r := &runner.RealRunner{
		DryRun: *dryRun,
	}

	// Channel to tell goroutines to stop.
	// Do not defer the close() because we want this stop to happen before other
	// defer functions.
	stopchan := make(chan struct{})

	// Clean leftovers from a previous build.
	if err := common.Clean(ctx, r); err != nil {
		return fmt.Errorf("Error cleaning: %v", err)
	}

	// Check installed docker versions.
	if !*dryRun {
		dockerServerVersion, dockerClientVersion, err := dockerVersions(ctx, r)
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
	// When the build is run locally, there will be no build ID. Assign a unique value.
	buildConfig.Id = "localbuild_" + uuid.New()

	// Get the ProjectId to feed both the build and the metadata server.
	// This command uses a runner without dryrun to return the real project.
	projectInfo, err := gcloud.ProjectInfo(ctx, &runner.RealRunner{})
	if err != nil {
		return fmt.Errorf("Error getting project information from gcloud: %v", err)
	}
	buildConfig.ProjectId = projectInfo.ProjectID

	// Parse substitutions.
	if *substitutions != "" {
		substMap, err := common.ParseSubstitutionsFlag(*substitutions)
		if err != nil {
			return fmt.Errorf("Error parsing substitutions flag: %v", err)
		}
		if err := validate.CheckSubstitutionsLoose(substMap); err != nil {
			return err
		}
		// Add any flag substitutions to the existing substitution map.
		// If the substitution is already defined in the template, the
		// substitutions flag will override the value.
		for k, v := range substMap {
			buildConfig.Substitutions[k] = v
		}
	}

	// Validate the build.
	if err := validate.CheckBuild(buildConfig); err != nil {
		return fmt.Errorf("Error validating build: %v", err)
	}
	// Do not accept built-in substitutions in the build config.
	if err := validate.CheckSubstitutions(buildConfig.Substitutions); err != nil {
		return fmt.Errorf("Error validating build's substitutions: %v", err)
	}

	// Apply substitutions.
	if err := subst.SubstituteBuildFields(buildConfig); err != nil {
		return fmt.Errorf("Error applying substitutions: %v", err)
	}

	// Validate the build after substitutions.
	if err := validate.CheckBuildAfterSubstitutions(buildConfig); err != nil {
		return fmt.Errorf("Error validating build after substitutions: %v", err)
	}

	// Create a volume, a helper container to copy the source, and defer cleaning.
	volumeName := fmt.Sprintf("%s%s", volumeNamePrefix, uuid.New())
	if !*dryRun {
		vol := volume.New(volumeName, r)
		if err := vol.Setup(ctx); err != nil {
			return fmt.Errorf("Error creating docker volume: %v", err)
		}
		if source != "" {
			// If the source is a directory, only copy the inner content.
			if isDir, err := isDirectory(source); err != nil {
				return fmt.Errorf("Error getting directory: %v", err)
			} else if isDir {
				source = filepath.Clean(source) + "/."
			}
			if err := vol.Copy(ctx, source); err != nil {
				return fmt.Errorf("Error copying source to docker volume: %v", err)
			}
		}
		defer vol.Close(ctx)
		if *writeWorkspace != "" {
			defer vol.Export(ctx, *writeWorkspace)
		}
	}

	b := build.New(r, *buildConfig, nil /* TokenSource */, stdoutLogger{}, nopEventLogger{}, volumeName, afero.NewOsFs(), true, *push, *dryRun)

	// Do not run the spoofed metadata server on a dryrun.
	if !*dryRun {
		// Set initial Docker credentials.
		tok, err := gcloud.AccessToken(ctx, r)
		if err != nil {
			return fmt.Errorf("Error getting access token to set docker credentials: %v", err)
		}
		if err := b.SetDockerAccessToken(ctx, tok.AccessToken); err != nil {
			return fmt.Errorf("Error setting docker credentials: %v", err)
		}
		b.TokenSource = oauth2.StaticTokenSource(&oauth2.Token{
			AccessToken: tok.AccessToken,
		})

		// On GCE, do not create a spoofed metadata server, use the existing one.
		// The cloudbuild network is still needed, with a private subnet.
		if computeMetadata.OnGCE() {
			if err := metadata.CreateCloudbuildNetwork(ctx, r, "172.22.0.0/16"); err != nil {
				return fmt.Errorf("Error creating network: %v", err)
			}
			defer metadata.CleanCloudbuildNetwork(ctx, r)
		} else {
			if err := metadata.StartLocalServer(ctx, r, metadataImageName); err != nil {
				return fmt.Errorf("Failed to start spoofed metadata server: %v", err)
			}
			log.Println("Started spoofed metadata server")
			metadataUpdater := metadata.RealUpdater{Local: true}
			defer metadataUpdater.Stop(ctx, r)

			// Feed the project info to the metadata server.
			metadataUpdater.SetProjectInfo(projectInfo)

			go supplyTokenToMetadata(ctx, metadataUpdater, r, stopchan)
		}

		// Write docker credentials for GCR. This writes the initial
		// ~/.docker/config.json, which is made available to build steps, and keeps
		// a fresh token available. Note that the user could `gcloud auth` to
		// switch accounts mid-build, and we wouldn't notice that until token
		// refresh; switching accounts mid-build is not supported.
		go func(tok *metadata.Token, stopchan <-chan struct{}) {
			for {
				refresh := time.Duration(0)
				if tok != nil {
					refresh = common.RefreshDuration(tok.Expiry)
				}

				select {
				case <-time.After(refresh):
					var err error
					tok, err = gcloud.AccessToken(ctx, r)
					if err != nil {
						log.Printf("Error getting access token to update docker credentials: %v\n", err)
						continue
					}

					if err := b.UpdateDockerAccessToken(ctx, tok.AccessToken); err != nil {
						log.Printf("Error updating docker credentials: %v", err)
					}

					b.TokenSource = oauth2.StaticTokenSource(&oauth2.Token{
						AccessToken: tok.AccessToken,
					})
				case <-stopchan:
					return
				}
			}
		}(tok, stopchan)
	}

	b.Start(ctx)
	<-b.Done

	close(stopchan)

	if b.GetStatus().BuildStatus == build.StatusError {
		return fmt.Errorf("Build finished with ERROR status")
	}

	if *dryRun {
		log.Printf("Warning: this was a dry run; add --dryrun=false if you want to run the build locally.")
	}
	return nil
}

// supplyTokenToMetadata gets gcloud token and supply it to the metadata server.
func supplyTokenToMetadata(ctx context.Context, metadataUpdater metadata.RealUpdater, r runner.Runner, stopchan <-chan struct{}) {
	for {
		tok, err := gcloud.AccessToken(ctx, r)
		if err != nil {
			log.Printf("Error getting gcloud token: %v", err)
			continue
		}
		if err := metadataUpdater.SetToken(tok); err != nil {
			log.Printf("Error updating token in metadata server: %v", err)
			continue
		}
		refresh := common.RefreshDuration(tok.Expiry)
		select {
		case <-time.After(refresh):
			continue
		case <-stopchan:
			return
		}
	}
}

// dockerVersion gets local server and client docker versions.
func dockerVersions(ctx context.Context, r runner.Runner) (string, string, error) {
	cmd := []string{"docker", "version", "--format", "{{.Server.Version}}"}
	var serverb bytes.Buffer
	if err := r.Run(ctx, cmd, nil, &serverb, os.Stderr, ""); err != nil {
		return "", "", err
	}

	cmd = []string{"docker", "version", "--format", "{{.Client.Version}}"}
	var clientb bytes.Buffer
	if err := r.Run(ctx, cmd, nil, &clientb, os.Stderr, ""); err != nil {
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

type stdoutLogger struct{}

func (stdoutLogger) MakeWriter(prefix string, _ int, stdout bool) io.Writer {
	if stdout {
		return prefixWriter{os.Stdout, prefix}
	}
	return prefixWriter{os.Stderr, prefix}
}
func (stdoutLogger) Close() error              { return nil }
func (stdoutLogger) WriteMainEntry(msg string) { fmt.Fprintln(os.Stdout, msg) }

type prefixWriter struct {
	w interface {
		WriteString(string) (int, error)
	}
	prefix string
}

func (pw prefixWriter) Write(b []byte) (int, error) {
	var buf bytes.Buffer
	if n, err := buf.Write(b); err != nil {
		return n, err
	}

	scanner := bufio.NewScanner(&buf)
	for scanner.Scan() {
		line := scanner.Text()
		line = fmt.Sprintf("%s: %s\n", pw.prefix, line)
		pw.w.WriteString(line)
	}
	if err := scanner.Err(); err != nil {
		return -1, err
	}
	return len(b), nil
}

type nopEventLogger struct{}

func (nopEventLogger) StartStep(context.Context, int, time.Time) error        { return nil }
func (nopEventLogger) FinishStep(context.Context, int, bool, time.Time) error { return nil }
