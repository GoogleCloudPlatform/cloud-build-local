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
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path"
	"reflect"
	"strings"

	pb "google.golang.org/genproto/googleapis/devtools/cloudbuild/v1"
	"github.com/GoogleCloudPlatform/container-builder-local/runner"
	"github.com/spf13/afero"
	"github.com/google/uuid"
)

const (
	srcIndex        = 0
	destIndex       = 1
	md5Index        = 4
	resultIndex     = 8
	descIndex       = 9
	filemode        = 0644                        // readable and writable files
	errFileNotFound = "No such file or directory" // printed to output when the Linux find command has no findings
)

var (
	// newUUID returns a new uuid string and is stubbed during testing.
	newUUID = uuid.New
	// csvHeaders are the CSV headers of a `gsutil cp` manifest file. See https://cloud.google.com/storage/docs/gsutil/commands/cp.
	csvHeaders = []string{"Source", "Destination", "Start", "End", "Md5", "UploadId", "Source Size", "Bytes Transferred", "Result", "Description"}
)

// Helper is a mockable interface for running gsutil commands in docker.
type Helper interface {
	VerifyBucket(ctx context.Context, bucket string) error
	UploadArtifacts(ctx context.Context, flags DockerFlags, src, dest string) ([]*pb.ArtifactResult, error)
	UploadArtifactsManifest(ctx context.Context, flags DockerFlags, manifest, bucket string, results []*pb.ArtifactResult) (string, error)
}

// RealHelper provides helper functions that actual run gsutil commands in docker.
type RealHelper struct {
	runner runner.Runner
	fs     afero.Fs
}

// New returns a new RealHelper struct.
func New(r runner.Runner, fs afero.Fs) RealHelper {
	return RealHelper{runner: r, fs: fs}
}

// VerifyBucket returns nil if the bucket exists, otherwise an error.
func (g RealHelper) VerifyBucket(ctx context.Context, bucket string) error {
	args := []string{"docker", "run",
		// Assign container name.
		"--name", fmt.Sprintf("cloudbuild_gsutil_ls_%s", newUUID()),
		// Remove the container when it exits.
		"--rm",
		// Make sure the container uses the correct docker daemon.
		"--volume", "/var/run/docker.sock:/var/run/docker.sock",
		// Connect to the network for metadata to get credentials.
		"--network", "cloudbuild",
		"gcr.io/cloud-builders/gsutil", "ls", bucket}

	if err := g.runner.Run(ctx, args, nil, ioutil.Discard, ioutil.Discard, ""); err == ctx.Err() {
		return err
	}
	// Running 'ls' on a bucket that doesn't exist or has an invalid GCS URL will return an error.
	return fmt.Errorf("bucket %q does not exist", bucket)
}

// DockerFlags holds information relevant to docker run invocations.
type DockerFlags struct {
	Workvol string
	Workdir string
	Tmpdir  string
}

// UploadArtifacts copies artifacts from the project workspace source to a GCS bucket.
// Returns the GCS path of the artifact manifest file and the number of artifacts uploaded.
func (g RealHelper) UploadArtifacts(ctx context.Context, flags DockerFlags, src, dest string) ([]*pb.ArtifactResult, error) {
	// We need a temporary directory for the gsutil manifest.
	if flags.Tmpdir == "" {
		return nil, fmt.Errorf("flags.Tmpdir has no value")
	}

	// Create a temp file for gsutil manifest. This manifest is used when making calls to "gsutil cp."
	// The user should not see the gsutil manifest after the upload.
	f := fmt.Sprintf("manifest_%s.log", newUUID())

	tmpfile, err := afero.TempFile(g.fs, flags.Tmpdir, f)
	if err != nil {
		return nil, err
	}
	defer tmpfile.Close()
	gsutilManifest := tmpfile.Name()

	// Find files in the workspace directory that match the glob string.
	srcFiles, err := g.glob(ctx, flags, src)
	if err != nil {
		return nil, fmt.Errorf("error finding matching files for %q: err = %v", src, err)
	}
	if len(srcFiles) == 0 {
		return nil, nil
	}

	// Copy matching files to the GCS bucket.
	
	
	
	for _, src := range srcFiles {
		if output, err := g.runGsutil(ctx, flags, "cp", "-L", gsutilManifest, src, adjustDest(src, dest)); err != nil {
			log.Printf("gsutil could not copy artifact %q to %q:\n%s", src, dest, output)
			return nil, err
		}
	}

	results, err := g.parseGsutilManifest(gsutilManifest)
	if err != nil {
		return nil, err
	}

	// Update ArtifactResult location fields to include a generation number in the filepath.
	for _, r := range results {
		newLoc, err := g.getGeneration(ctx, flags, r.Location)
		if err != nil {
			return nil, err
		}
		r.Location = newLoc
	}

	return results, nil
}

func (g RealHelper) UploadArtifactsManifest(ctx context.Context, flags DockerFlags, manifest, bucket string, results []*pb.ArtifactResult) (string, error) {
	// Write the artifacts manifest file locally in a temporary directory.
	manifestPath := path.Join(flags.Tmpdir, manifest)
	if err := g.createArtifactsManifest(manifestPath, results); err != nil {
		return "", err
	}
	defer g.fs.Remove(manifestPath)

	// Upload manifest to the GCS bucket.
	if output, err := g.runGsutil(ctx, flags, "cp", manifestPath, bucket); err != nil {
		log.Printf("gsutil could not copy artifact manifest %q to %q:\n%s", manifestPath, bucket, output)
		return "", err
	}

	// Remove any trailing forward slash in GCS bucket URL. GCS accepts URLs with or without a trailing slash.
	// We don't want the path to have a double slash.
	
	b := strings.TrimSuffix(bucket, "/")
	return strings.Join([]string{b, manifest}, "/"), nil
}

// createArtifactsManifest writes a list of ArtifactResult to a JSON manifest. The JSON manifest may be empty.
func (g RealHelper) createArtifactsManifest(manifestPath string, results []*pb.ArtifactResult) error {
	
	f, err := g.fs.OpenFile(manifestPath, os.O_RDWR|os.O_CREATE, filemode)
	if err != nil {
		return err
	}
	defer f.Close()

	for _, r := range results {
		if err := json.NewEncoder(f).Encode(r); err != nil {
			return err
		}
	}

	return nil
}

// glob searches the workspace directory for source files that match the glob src string, and returns a string array of file paths.
func (g RealHelper) glob(ctx context.Context, flags DockerFlags, src string) ([]string, error) {
	
	if flags.Workvol == "" {
		return nil, errors.New("flags.Workvol has no value")
	}
	if flags.Workdir == "" {
		return nil, errors.New("flags.Workdir has no value")
	}

	// Docker run args for gsutil.
	args := []string{"docker", "run",
		// Assign container name.
		"--name", fmt.Sprintf("cloudbuild_gsutil_%s", newUUID()),
		// Remove the container when it exits.
		"--rm",
		// Mount the project workspace,
		"--volume", flags.Workvol,
		// Run gsutil from the workspace dir.
		"--workdir", flags.Workdir,
		// Set bash entrypoint to enable multiple gsutil commands.
		"--entrypoint", "bash"}

	args = append(args, "ubuntu")
	args = append(args, "-c")
	// Enable globbing and find files that match the glob string. This will include hidden files.
	// We prefix source with "./" so that wildcarding works. Why does "./" make a difference? No idea. ¯\_(ツ)_/¯
	// Note that resulting filepath matches will also have "./"	prefixed.
	
	args = append(args, fmt.Sprintf("shopt -s globstar; find ./%s -type f", src))

	output, err := g.runAndScrape(ctx, args)
	if err != nil {
		if strings.Contains(output, errFileNotFound) {
			// The 'find' command will return an error if there are no matching files. Suppress the error and return empty string slice.
			log.Printf("no files match glob string %q", src)
			return []string{}, nil
		}
		return []string{}, err
	}

	// Remove leading and trailing newlines so the split doesn't result in empty string elements.
	return strings.Split(strings.Trim(output, "\n"), "\n"), nil
}

func (g RealHelper) runGsutil(ctx context.Context, flags DockerFlags, cmd ...string) (string, error) {
	if flags.Workvol == "" {
		return "", errors.New("flags.Workvol has no value")
	}
	if flags.Workdir == "" {
		return "", errors.New("flags.Workdir has no value")
	}

	// Docker run args for gsutil.
	args := []string{"docker", "run",
		// Assign container name.
		"--name", fmt.Sprintf("cloudbuild_gsutil_%s", newUUID()),
		// Remove the container when it exits.
		"--rm",
		// Make sure the container uses the correct docker daemon.
		"--volume", "/var/run/docker.sock:/var/run/docker.sock",
		// Mount the project workspace,
		"--volume", flags.Workvol,
		// Run gsutil from the workspace dir.
		"--workdir", flags.Workdir,
		// Connect to the network for metadata to get credentials.
		"--network", "cloudbuild"}
	if flags.Tmpdir != "" {
		// Mount the temporary directory.
		args = append(args, []string{"--volume", fmt.Sprintf("%s:%s", flags.Tmpdir, flags.Tmpdir)}...)
	}
	// Add gsutil docker image and commands.
	args = append(args, "gcr.io/cloud-builders/gsutil")
	args = append(args, cmd...)

	// We return the string output from the command run, but it's only intended to be used for debugging and testing.
	return g.runAndScrape(ctx, args)
}

// getGeneration takes a GCS object URL as input and returns the URL with the generation number suffixed.
func (g RealHelper) getGeneration(ctx context.Context, flags DockerFlags, url string) (string, error) {
	
	// If we uploaded a large amount of artifacts, we'd have to call this many times to get the generations of all these files.
	// Moreover, if we wait after upload to check all the file generations, it's possible that the file can change.

	// List existing object with generation number information.
	// See https://cloud.google.com/storage/docs/gsutil/commands/ls.
	output, err := g.runGsutil(ctx, flags, "ls", "-a", url)
	if err != nil {
		return "", err
	}
	// Get the largest generation number, which indicates the most recent generation.
	// `gsutil ls -a` should return a list of object generations in increasing order, with
	// the most recent generation in the last line of output.
	//
	// Example output:
	// $ gsutil ls -a gs://bucket/some/path/output.jar
	//		> gs://bucket/some/path/output.jar#10000100010001
	//		> gs://bucket/some/path/output.jar#10000100010002
	lines := strings.Split(output, "\n")

	// Search the lines for the largest generation number.
	// If gsutil changes the way it prints to standard output, we should catch it.
	max := ""
	for _, l := range lines {
		if l > max {
			max = l
		}
	}
	if max == "" {
		return max, fmt.Errorf("could not find most recent generation for %q", url)
	}

	return max, nil
}

// parseGsutilManifest parses the items of a gsutil cp manifest file. It returns a list of ArtifactResult corresponding to the files copied to GCS.
// If any error or skip statuses are detected, it returns an error.
func (g RealHelper) parseGsutilManifest(manifestPath string) ([]*pb.ArtifactResult, error) {
	
	f, err := g.fs.Open(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("could not open %s: %v", manifestPath, err)
	}

	r := csv.NewReader(f)
	r.FieldsPerRecord = len(csvHeaders)
	values, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("could not read %s: err = %v", manifestPath, err)
	}
	if !reflect.DeepEqual(values, csvHeaders) {
		return nil, fmt.Errorf("got csv headers = %+v,\nwant %+v", values, csvHeaders)
	}

	artifacts := []*pb.ArtifactResult{}
	for {
		values, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		switch result := values[resultIndex]; result {
		case "error":
			// Return an error if any file fails upload.
			return nil, fmt.Errorf("error copying %s to %s: %v", values[srcIndex], values[destIndex], values[descIndex])
		case "skip":
			// Return an error if any file is skipped for copy. Files are only skipped when 'gsutil cp' is called with a no-clobber flag enabled,
			// which we do not use.
			return nil, fmt.Errorf("skipped copying %s to %s: %v", values[srcIndex], values[destIndex], values[descIndex])
		default:
			// Success. The result of successful items is either 'OK' or an empty field.
			artifacts = append(artifacts, &pb.ArtifactResult{
				Location: values[destIndex],
				FileHash: []*pb.FileHashes{{
					FileHash: []*pb.Hash{{Type: pb.Hash_MD5, Value: []byte(values[md5Index])}}},
				},
			})
		}
	}
	return artifacts, nil
}

// runAndScrape executes the command and returns the output (stdin, stderr), without logging.
func (g RealHelper) runAndScrape(ctx context.Context, cmd []string) (string, error) {
	
	var buf bytes.Buffer
	outWriter := io.Writer(&buf)
	errWriter := io.Writer(&buf)
	err := g.runner.Run(ctx, cmd, nil, outWriter, errWriter, "")
	return buf.String(), err
}

// adjustDest takes any leading directory filepath from src and suffixes it to dest.
//     e.g. adjustDest("/nested/path/test.xml", "gs://some-bucket/dir/") = "gs://some-bucket/dir/nested/path/"
//
// Why? The 'gsutil cp' command copies a file from a local directory to a remote GCS bucket. https://cloud.google.com/storage/docs/gsutil/commands/cp
// The local file and GCS bucket are specified with file paths.
// Consider the following:
//			source = "/nested/path/test.xml" # source path
//      dest   = "gs://some-bucket/dir/" # bucket path
//         > gsutil cp source dest
// The resulting GCS object path will be "gs://some-bucket/dir/test.xml". This is problematic when we want to copy multiple source
// files with the same name that span different directories. The gsutil tool will ignore the source file's leading directory, and
// will copy all matching files to the same GCS object path. To avoid filename collision and overwriting, we maintain the directory structure
// by adjusting the bucket path to include the source file's leading directory.
func adjustDest(src, dest string) string {
	
	leadDir := path.Dir(src)
	if len(leadDir) > 1 {
		// Note: dest already has trailing slash, and if we do path.Join(), "gs://" will lose a slash.
		return fmt.Sprintf("%s%s/", dest, strings.TrimPrefix(leadDir, "/"))
	}
	return dest
}
