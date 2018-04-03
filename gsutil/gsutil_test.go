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

package gsutil

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"testing"

	pb "google.golang.org/genproto/googleapis/devtools/cloudbuild/v1"

	"google3/net/proto2/go/proto"
	"github.com/spf13/afero"
	"github.com/google/uuid"
)

var joinedHeaders = strings.Join(csvHeaders, ",")

type mockRunner struct {
	
	t                 *testing.T
	testCaseName      string
	commands          []string
	buckets           []string
	objects           map[string][]string // maps GCS bucket names to a list of GCS objects
	objectGenerations map[string][]string // maps GCS objects to their generation numbers
	manifestFile      string
	stdout            string // when specified, the runner will write this string to stdout
	stderr            string // when specified, the runner will write this string to stderr
	fs                afero.Fs
}

func newMockRunner(t *testing.T, testCaseName string) *mockRunner {
	return &mockRunner{
		t:            t,
		testCaseName: testCaseName,
		// Default buckets.
		buckets: []string{
			"gs://bucket-one",
			"gs://bucket-two",
			"gs://bucket-three",
		},
		// Default objects.
		objects: map[string][]string{
			"gs://bucket-one":   {"hall.json"},
			"gs://bucket-two":   {"maraudersmap.jpg", "passwords.txt"},
			"gs://bucket-three": {"bell.jar", "google.doc", "html.html"},
		},
		// Default objects mapped to version generation numbers.
		// Generation numbers are 16 digits long but are simplified for testing.
		// https://cloud.google.com/storage/docs/object-versioning
		objectGenerations: map[string][]string{
			"gs://bucket-one/hall.json":     {"1000"},
			"gs://bucket-two/passwords.txt": {"2003", "2004", "2002"},
		},
		// Default manifest file.
		manifestFile: "Source,Destination,Start,End,Md5,UploadId,Source Size,Bytes Transferred,Result,Description\r" +
			"file://one.txt,gs://bucket/one.txt,,,,,,,,\r" +
			"file://two.txt,gs://bucket/two.txt,,,,,,,,\r" +
			"file://three.txt,gs://bucket/three.txt,,,,,,,OK,\r\n",
	}
}

// startsWith returns true iff arr startsWith parts.

func startsWith(arr []string, parts ...string) bool {
	if len(arr) < len(parts) {
		return false
	}
	for i, p := range parts {
		if arr[i] != p {
			return false
		}
	}
	return true
}

// contains returns true iff arr contains parts in order, considering only the
// first occurrence of the first part.

func contains(arr []string, parts ...string) bool {
	if len(arr) < len(parts) {
		return false
	}
	for i, a := range arr {
		if a == parts[0] {
			return startsWith(arr[i:], parts...)
		}
	}
	return false
}

func (r *mockRunner) MkdirAll(dir string) error {
	r.commands = append(r.commands, fmt.Sprintf("MkdirAll(%s)", dir))
	return nil
}

func (r *mockRunner) WriteFile(path, contents string) error {
	r.commands = append(r.commands, fmt.Sprintf("WriteFile(%s,%q)", path, contents))
	return nil
}

func (r *mockRunner) Clean() error {
	return nil
}

// gsutil simulates gsutil commands in the mockrunner.
func (r *mockRunner) gsutil(args []string, in io.Reader, out, err io.Writer) error {
	if startsWith(args, "ls") {
		// Simulate 'gsutil ls' command (https://cloud.google.com/storage/docs/gsutil/commands/ls).
		lastArg := args[len(args)-1]

		if lastArg == "ls" {
			// List all the buckets
			for i, b := range r.buckets {
				io.WriteString(out, b)
				// Do not print new line after last bucket.
				if i < len(r.buckets)-1 {
					io.WriteString(out, "\n")
				}
			}
			return nil
		}

		// The last arg is a URL. Check that the URL is prefixed with 'gs://'.
		url := lastArg
		if !strings.HasPrefix(url, "gs://") {
			return fmt.Errorf("invalid URL: %q does not start with gs://", url)
		}
		// A bucket path splits into 3 strings, while an object path would split into 4+ strings.
		if len(strings.SplitN(url, "/", 4)) < 4 {
			// The URL is for a bucket. Check if it exists.
			objects, ok := r.objects[url]
			if !ok {
				return fmt.Errorf("bucket %q does not exist", url)
			}
			for _, obj := range objects {
				io.WriteString(out, strings.Join([]string{url, obj}, "/"))
			}
			return nil
		}

		// The URL is for an object. Check if it exists.
		versions, ok := r.objectGenerations[url]
		if !ok {
			return fmt.Errorf("URL %q matches no objects", url)
		}
		if contains(args, "-a") {
			for _, v := range versions {
				// Print all versions of that URL.
				fmt.Fprintln(out, url+"#"+v)
			}
		} else {
			io.WriteString(out, url)
		}

		return nil
	}
	if startsWith(args, "cp") {
		// Simulate 'gsutil cp' command (https://cloud.google.com/storage/docs/gsutil/commands/cp).
		if contains(args, "-L") {
			// -L option is present when gsutil copies source file to destination bucket.
			// We won't simulate copying files, but we'll simulate the results by writing a manifest.
			manifestIdx := -1
			for i, a := range args {
				if a == "-L" {
					manifestIdx = i + 1
					break
				}
			}
			if manifestIdx < 0 || manifestIdx > len(args) {
				return errors.New("-L option has no manifest path specified")
			}

			// Verify source file to copy exists in our mocked local environment.
			// We won't do wildcard/regex matching here, so tests should specify a file.
			src := args[len(args)-2]
			exists, err := afero.Exists(r.fs, src)
			if err != nil {
				return err
			}
			if !exists {
				errStr := fmt.Sprintf("No URLs matched: %s", src)
				io.WriteString(out, errStr)
				return errors.New(errStr)
			}

			return afero.WriteFile(r.fs, args[manifestIdx], []byte(r.manifestFile), filemode)
		}
		// If no -L option, recognize cp as a valid command but do nothing. We do not need it for tests.
		// This happens with TestUploadArtifacts.
		r.t.Logf("note: mockrunner accepts gsutil args %+v, but will do nothing", args)
		return nil
	}
	return fmt.Errorf("mockRunner does not support this gsutil command: %+v", args)

}

func (r *mockRunner) Run(ctx context.Context, args []string, in io.Reader, out, errW io.Writer, _ string) error {
	r.commands = append(r.commands, strings.Join(args, " "))

	// If a stderr or stdout is specified, print it out and skip checking for specific arguments.
	if len(r.stderr) > 0 {
		io.WriteString(errW, r.stderr)
		return errors.New(r.stderr)
	}
	if len(r.stdout) > 0 {
		io.WriteString(out, r.stdout)
		return nil
	}

	if startsWith(args, "docker", "run") {
		if contains(args, "ubuntu", "cat") {
			io.WriteString(out, r.manifestFile)
			return nil
		}
		if contains(args, "ubuntu", "-c") {
			// Extract the source string.
			cmdRegex := `shopt -s globstar; find ./.* -type f`
			cmd := ""
			for _, a := range args {
				matched, err := regexp.MatchString(cmdRegex, a)
				if err != nil {
					return fmt.Errorf("mockrunner: regexp.MatchString(%q, %q): %v", cmdRegex, a, err)
				}
				if matched {
					cmd = a
					break
				}
			}
			if len(cmd) == 0 {
				return fmt.Errorf("mockrunner does not support the string command passed to -c: args = %+v", args)
			}
			// If that file exists, print the filepath to stdout.
			// Note that mockrunner does not simulate output for glob strings with wildcards.
			src := strings.Split(cmd, " ")[4]
			exists, err := afero.Exists(r.fs, src)
			if err != nil {
				return err
			}
			if !exists {
				errStr := fmt.Sprintf("%s: %s", src, errFileNotFound)
				io.WriteString(out, errStr)
				return errors.New(errStr)
			}
			io.WriteString(out, src)
			return nil
		}

		// Look for gcr.io/cloud-builders image.
		for i, a := range args {
			if a == "gcr.io/cloud-builders/gsutil" {
				return r.gsutil(args[i+1:], in, out, errW)
			}
		}
		return fmt.Errorf("mockrunner does not support the docker image in args: %+v", args)
	}

	return nil
}

// checkCommands verifies that the commands run in mockrunner match the expected commands.
func checkCommands(commands []string, wantCommands []string) error {
	var errors []string
	addError := func(e error) { errors = append(errors, e.Error()) }

	if len(commands) != len(wantCommands) {
		addError(fmt.Errorf("Wrong number of commands: got %d, want %d", len(commands), len(wantCommands)))
	}
	for i := range commands {
		if wantCommands[i] != commands[i] {
			addError(fmt.Errorf("command %d didn't match! \n===Want:\n%q\n===Got:\n%q", i, wantCommands[i], commands[i]))
		}
	}
	got := strings.Join(commands, "\n")
	want := strings.Join(wantCommands, "\n")
	if want != got {
		addError(fmt.Errorf("Commands didn't match!\n===Want:\n%q\n===Got:\n%q", want, got))
	}

	if len(errors) > 0 {
		return fmt.Errorf(strings.Join(errors, "\n"))
	}
	return nil
}

func TestVerifyBucket(t *testing.T) {
	ctx := context.Background()
	newUUID = func() string { return "someuuid" }
	defer func() { newUUID = uuid.New }()

	testCases := []struct {
		name    string
		bucket  string
		wantErr bool
	}{{
		name:   "Exists",
		bucket: "gs://bucket-one",
	}, {
		name:    "DoesNotExist",
		bucket:  "gs://i-do-not-exist",
		wantErr: true,
	}, {
		name:    "InvalidName",
		bucket:  "i-am-not-a-gcs-url",
		wantErr: true,
	}}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := newMockRunner(t, tc.name)

			gsutilHelper := New(r, afero.NewMemMapFs())
			err := gsutilHelper.VerifyBucket(ctx, tc.bucket)
			if err == nil && tc.wantErr {
				t.Errorf("got gsutilHelper.VerifyBucket(%s) = %v, want error", tc.bucket, err)
			} else if err != nil && !tc.wantErr {
				t.Errorf("got gsutilHelper.VerifyBucket(%s) = %v, want nil", tc.bucket, err)
			}

			// Check docker run commands and arguments.
			wantCommands := []string{
				"docker run --name cloudbuild_gsutil_ls_" + newUUID() +
					" --rm --volume /var/run/docker.sock:/var/run/docker.sock --network cloudbuild gcr.io/cloud-builders/gsutil ls " + tc.bucket,
			}
			if err := checkCommands(r.commands, wantCommands); err != nil {
				t.Errorf("checkCommands(): err = \n%v", err)
			}
		})
	}
}

func TestUploadArtifacts(t *testing.T) {
	
	ctx := context.Background()
	md5 := "md5"
	fakeFileHashes := []*pb.FileHashes{{FileHash: []*pb.Hash{{Type: pb.Hash_MD5, Value: []byte(md5)}}}}

	testCases := []struct {
		name              string
		flags             DockerFlags
		manifestItems     []string
		objectGenerations map[string]string
		localFiles        []string
		source            string // NB: mockrunner cannot do wildcard matching, so specify a single filename
		wantResults       []*pb.ArtifactResult
		wantError         bool
	}{{
		name:  "Success",
		flags: DockerFlags{Workvol: "workvol", Workdir: "workdir", Tmpdir: "tmpdir"},
		manifestItems: []string{joinedHeaders,
			toCsv("gs://bucket-some/one.txt", md5, "OK"),
			toCsv("gs://bucket-some/two.txt", md5, "OK"),
		},
		objectGenerations: map[string]string{
			"gs://bucket-some/one.txt": "1000",
			"gs://bucket-some/two.txt": "2000",
		},
		localFiles: []string{"one.txt"},
		source:     "one.txt",
		wantResults: []*pb.ArtifactResult{
			{Location: "gs://bucket-some/one.txt#1000", FileHash: fakeFileHashes},
			{Location: "gs://bucket-some/two.txt#2000", FileHash: fakeFileHashes},
		},
	}, {
		name:          "SuccessNoArtifactsUploaded",
		flags:         DockerFlags{Workvol: "workvol", Workdir: "workdir", Tmpdir: "tmpdir"},
		source:        "idonotexist.yaml",
		manifestItems: []string{joinedHeaders}, // empty manifest
	}, {
		name:   "SuccessSourceDoesNotExist", // same result as NoArtifactsUploaded
		flags:  DockerFlags{Workvol: "workvol", Workdir: "workdir", Tmpdir: "tmpdir"},
		source: "idonotexist.yaml",
	}, {
		name:      "ErrorNoTmpdir",
		flags:     DockerFlags{Workvol: "workvol", Workdir: "workdir"},
		source:    "one.txt",
		wantError: true,
	}}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			r := newMockRunner(t, tc.name)
			r.manifestFile = toManifest(tc.manifestItems...)
			r.fs = fs
			gsutilHelper := New(r, fs)

			for k, v := range tc.objectGenerations {
				// We add the objectGenerations to the runner objectGenerations map otherwise
				// the mockrunner's 'gsutil ls -a' call will return an error that the object does not exist.
				r.objectGenerations[k] = []string{v}
			}

			for _, f := range tc.localFiles {
				if _, err := fs.Create(f); err != nil {
					t.Fatal(err)
				}
			}

			// NB: The destination bucket's existence is checked before any artifacts are uploaded, so it's value here does not matter.
			results, err := gsutilHelper.UploadArtifacts(ctx, tc.flags, tc.source, "gs://some-bucket")
			if tc.wantError {
				if err == nil {
					t.Fatal("UploadArtifacts(): got err = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("UploadArtifacts(): err = %v", err)
			}

			if len(results) != len(tc.wantResults) {
				t.Fatalf("got %d results, want %d", len(results), len(tc.wantResults))
			}
			for i, item := range results {
				if !proto.Equal(tc.wantResults[i], item) {
					t.Errorf("got results[%d] = %+v,\nwant %+v", i, item, tc.wantResults[i])
				}
			}

			// For gcr.io/cloud-builders/gsutil cp commmands run in a docker container, a "./" must be prefixed to the source URL in order for gsutil wildcarding to work.
			wantSource := fmt.Sprintf("./%s", tc.source)
			for _, c := range r.commands {
				if strings.Contains(c, "cp") && strings.Contains(c, tc.source) && !strings.Contains(c, wantSource) {
					t.Errorf("got source = %q, only want source %s; all source URLs must be prefixed with %q: args =[%+v]", tc.source, wantSource, "./", c)
				}
			}
		})
	}
}

func TestGlob(t *testing.T) {
	ctx := context.Background()
	newUUID = func() string { return "someuuid" }
	defer func() { newUUID = uuid.New }()

	testCases := []struct {
		name         string
		flags        DockerFlags
		src          string // glob string
		stdout       string // specifies standard output in the mockRunner
		stderr       string // specifies error output in mockRunner
		wantFiles    []string
		wantCommands []string
		wantError    bool
	}{{
		name:      "OneMatchingFile",
		flags:     DockerFlags{Workvol: "workvol", Workdir: "workdir"},
		src:       "foo.xml",
		stdout:    "foo.xml\n",
		wantFiles: []string{"foo.xml"},
		wantCommands: []string{
			"docker run --name cloudbuild_gsutil_" + newUUID() +
				" --rm --volume workvol --workdir workdir --entrypoint bash ubuntu -c" +
				" shopt -s globstar; find ./foo.xml -type f",
		},
	}, {
		name:      "MultipleMatchingFiles",
		flags:     DockerFlags{Workvol: "workvol", Workdir: "workdir"},
		src:       "*.xml",
		stdout:    "foo.xml\nbar.xml\n",
		wantFiles: []string{"foo.xml", "bar.xml"},
		wantCommands: []string{
			"docker run --name cloudbuild_gsutil_" + newUUID() +
				" --rm --volume workvol --workdir workdir --entrypoint bash ubuntu -c" +
				" shopt -s globstar; find ./*.xml -type f",
		},
	}, {
		name:   "NoMatchingFiles",
		flags:  DockerFlags{Workvol: "workvol", Workdir: "workdir"},
		src:    "idonotexist.xml",
		stderr: "idonotexist.xml: No such file or directory\n",
		wantCommands: []string{
			"docker run --name cloudbuild_gsutil_" + newUUID() +
				" --rm --volume workvol --workdir workdir --entrypoint bash ubuntu -c" +
				" shopt -s globstar; find ./idonotexist.xml -type f",
		},
	}, {
		name:      "ErrorMissingWorkvol",
		flags:     DockerFlags{Workdir: "workdir"},
		wantError: true,
	}, {
		name:      "ErrorMissingWorkdir",
		flags:     DockerFlags{Workvol: "workvol"},
		wantError: true,
	}}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := newMockRunner(t, tc.name)
			r.stdout = tc.stdout
			r.stderr = tc.stderr
			gsutilHelper := New(r, afero.NewMemMapFs())

			files, err := gsutilHelper.glob(ctx, tc.flags, tc.src)
			if tc.wantError {
				if err == nil {
					t.Errorf("glob(): got err = nil, want error")
				}
				return // desired behavior
			}
			if err != nil {
				t.Errorf("glob(): err := %v", err)
			}
			if err := checkCommands(r.commands, tc.wantCommands); err != nil {
				t.Errorf("checkCommands(): err = \n%v", err)
			}
			if len(files) != len(tc.wantFiles) {
				t.Fatalf("got %d files = %+v, want %d files = %+v", len(files), files, len(tc.wantFiles), tc.wantFiles)
			}
			for i, f := range files {
				if f != tc.wantFiles[i] {
					t.Errorf("got file[%d]  = %q, want %q", i, f, tc.wantFiles[i])
				}
			}
		})
	}
}

func TestRunGsutil(t *testing.T) {
	ctx := context.Background()
	newUUID = func() string { return "someuuid" }
	defer func() { newUUID = uuid.New }()

	testCases := []struct {
		name         string
		flags        DockerFlags
		commands     []string
		wantCommands []string
		wantError    bool
	}{{
		name:     "HappyCaseOneArg",
		flags:    DockerFlags{Workvol: "workvol", Workdir: "workdir"},
		commands: []string{"ls"},
		wantCommands: []string{
			"docker run --name cloudbuild_gsutil_" + newUUID() +
				" --rm --volume /var/run/docker.sock:/var/run/docker.sock --volume workvol --workdir workdir --network cloudbuild" +
				" gcr.io/cloud-builders/gsutil ls",
		},
	}, {
		name:     "HappyCaseMultiArgs",
		flags:    DockerFlags{Workvol: "workvol", Workdir: "workdir"},
		commands: []string{"ls", "gs://bucket-one"},
		wantCommands: []string{
			"docker run --name cloudbuild_gsutil_" + newUUID() +
				" --rm --volume /var/run/docker.sock:/var/run/docker.sock --volume workvol --workdir workdir --network cloudbuild" +
				" gcr.io/cloud-builders/gsutil ls gs://bucket-one",
		},
	}, {
		name:     "HappyCaseTmpDir",
		flags:    DockerFlags{Workvol: "workvol", Workdir: "workdir", Tmpdir: "tmpdir"},
		commands: []string{"ls"},
		wantCommands: []string{
			"docker run --name cloudbuild_gsutil_" + newUUID() +
				" --rm --volume /var/run/docker.sock:/var/run/docker.sock --volume workvol --workdir workdir --network cloudbuild" +
				" --volume " + "tmpdir:tmpdir" +
				" gcr.io/cloud-builders/gsutil ls",
		},
	}, {
		name:      "ErrorMissingWorkvol",
		flags:     DockerFlags{Workdir: "workdir"},
		wantError: true,
	}, {
		name:      "ErrorMissingWorkdir",
		flags:     DockerFlags{Workvol: "workvol"},
		wantError: true,
	}, {
		name:      "ErrorNoGsutilCommand",
		flags:     DockerFlags{Workvol: "workvol", Workdir: "workdir"},
		wantError: true,
	}, {
		name:  "ErrorInvalidGsutilCommand",
		flags: DockerFlags{Workvol: "workvol", Workdir: "workdir"},
		// Use a command not supported by mockrunner.
		commands:  []string{"iamnotacommand"},
		wantError: true,
	}}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := newMockRunner(t, tc.name)
			gsutilHelper := New(r, afero.NewMemMapFs())

			output, err := gsutilHelper.runGsutil(ctx, tc.flags, tc.commands...)
			if tc.wantError {
				if err == nil {
					t.Errorf("runGsutil(): got err = nil, want error")
				}
				return // desired behavior
			}
			if err != nil {
				t.Errorf("runGsutil(): err := %v\n gsutil output:\n%s", err, output)
			}
			if err := checkCommands(r.commands, tc.wantCommands); err != nil {
				t.Errorf("checkCommands(): err = \n%v", err)
			}
		})
	}
}

func TestUploadArtifactsManifest(t *testing.T) {
	ctx := context.Background()
	flags := DockerFlags{Workvol: "workvol", Workdir: "workdir"}
	bucket := "gs://bucket-one"
	manifest := "manifest.log"
	results := []*pb.ArtifactResult{}

	wantManifestPath := "gs://bucket-one/manifest.log"

	r := newMockRunner(t, "TestUploadArtifactsManifest")
	gsutilHelper := New(r, afero.NewMemMapFs())
	manifestPath, err := gsutilHelper.UploadArtifactsManifest(ctx, flags, manifest, bucket, results)
	if err != nil {
		t.Fatalf("UploadArtifactsManifest(): err = %v", err)
	}

	if manifestPath != wantManifestPath {
		t.Errorf("got manifestPath = %s, want %s", manifestPath, wantManifestPath)
	}
}

func TestGetGeneration(t *testing.T) {
	ctx := context.Background()
	newUUID = func() string { return "someuuid" }
	defer func() { newUUID = uuid.New }()

	testCases := []struct {
		name      string
		url       string
		wantURL   string
		wantError bool
	}{{
		name:    "OneVersion",
		url:     "gs://bucket-one/hall.json",
		wantURL: "gs://bucket-one/hall.json#1000",
	}, {
		name:    "ManyVersions",
		url:     "gs://bucket-two/passwords.txt",
		wantURL: "gs://bucket-two/passwords.txt#2004",
	}, {
		name:      "ErrorURLNotFound",
		url:       "gs://bucket-one/idonotexist",
		wantError: true,
	}, {
		name:      "ErrorBlankURLNotFound",
		url:       "",
		wantError: true,
	}}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := newMockRunner(t, tc.name)
			gsutilHelper := New(r, afero.NewMemMapFs())

			url, err := gsutilHelper.getGeneration(ctx, DockerFlags{Workvol: "workvol", Workdir: "workdir"}, tc.url)

			wantCommands := []string{
				"docker run --name cloudbuild_gsutil_" + newUUID() +
					" --rm --volume /var/run/docker.sock:/var/run/docker.sock --volume workvol --workdir workdir --network cloudbuild gcr.io/cloud-builders/gsutil ls -a " + tc.url,
			}
			if err := checkCommands(r.commands, wantCommands); err != nil {
				t.Errorf("checkCommands(): err = \n%v", err)
			}

			if tc.wantError {
				if err == nil {
					t.Fatal("got nil error, want error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if url != tc.wantURL {
				t.Errorf("got url = %q, want %q", url, tc.wantURL)
			}
		})
	}
}

func toCsv(dest string, md5 string, result string) string {
	// CSV headers are defined in package-level var csvHeaders.
	return fmt.Sprintf(",%s,,,%s,,,,%s,", dest, md5, result)
}

func toManifest(lines ...string) string {
	return strings.Join(lines, "\n") + "\n"
}

func TestParseGsutilManifest(t *testing.T) {
	filepath := "iamamanifest"

	// md5ash is the fake md5 hash used throughout the test.
	md5Hash := "md5"
	fakeCopiedItems := []string{
		toCsv("gs://bucket/foo.txt", md5Hash, ""),
		toCsv("gs://bucket/bar.txt", md5Hash, "OK"),
	}
	fakeFileHashes := []*pb.FileHashes{{FileHash: []*pb.Hash{{Type: pb.Hash_MD5, Value: []byte(md5Hash)}}}}

	testCases := []struct {
		name          string
		manifest      string
		wantManifest  string
		wantResults   []*pb.ArtifactResult
		wantError     bool
		fileIsMissing bool
	}{{
		name:         "Success",
		manifest:     toManifest(joinedHeaders, fakeCopiedItems[0], fakeCopiedItems[1]),
		wantManifest: toManifest(joinedHeaders, fakeCopiedItems[0], fakeCopiedItems[1]),
		wantResults: []*pb.ArtifactResult{
			{Location: "gs://bucket/foo.txt", FileHash: fakeFileHashes},
			{Location: "gs://bucket/bar.txt", FileHash: fakeFileHashes},
		},
	}, {
		name:      "SkipStatus",
		manifest:  toManifest(joinedHeaders, toCsv("gs://bucket/two.txt", "md5", "skip")),
		wantError: true,
	}, {
		name:      "ErrorStatus",
		manifest:  toManifest(joinedHeaders, toCsv("gs://bucket/two.txt", "md5", "error")),
		wantError: true,
	}, {
		name:      "NoCSVHeader",
		manifest:  toManifest("thisisnotacsvheader"),
		wantError: true,
	}, {
		// When a field value has a comma, the CSV reader will split the field into two fields.
		name:      "WrongNumberOfFields",
		manifest:  toManifest(joinedHeaders, toCsv("", "", ",")),
		wantError: true,
	}, {
		name:      "ManifestIsEmpty",
		manifest:  "",
		wantError: true,
	}, {
		name:          "ManifestDoesNotExist",
		manifest:      toManifest(joinedHeaders, toCsv("", "", ",")),
		wantError:     true,
		fileIsMissing: true,
	}}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()

			if !tc.fileIsMissing {
				if _, err := fs.Create(filepath); err != nil {
					t.Fatal(err)
				}
				afero.WriteFile(fs, filepath, []byte(tc.manifest), filemode)
			}
			r := newMockRunner(t, tc.name)
			gsutilHelper := New(r, fs)
			results, err := gsutilHelper.parseGsutilManifest(filepath)

			if tc.wantError {
				if err == nil {
					t.Fatal("got nil error, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("%v", err)
			}
			for i, item := range results {
				if !proto.Equal(tc.wantResults[i], item) {
					t.Errorf("parseGsutilManifest() returned wrong result for results[%d]; got %+v, want %+v", i, item, tc.wantResults[i])
				}
			}
		})
	}
}

func TestCreateArtifactsManifest(t *testing.T) {
	manifestPath := "iamamanifest"
	md5 := []byte("md5hash")
	md5base64 := base64.StdEncoding.EncodeToString(md5) // JSON encoder will write base-64 string of md5 hash
	fakeFileHashes := []*pb.FileHashes{{FileHash: []*pb.Hash{{Type: pb.Hash_MD5, Value: md5}}}}

	testCases := []struct {
		name         string
		results      []*pb.ArtifactResult
		wantManifest string
		wantErr      bool
	}{{
		name: "Success",
		results: []*pb.ArtifactResult{
			{Location: "gs://bucket/foo.txt", FileHash: fakeFileHashes},
			{Location: "gs://bucket/bar.txt", FileHash: fakeFileHashes},
		},
		wantManifest: `{"location":"gs://bucket/foo.txt","file_hash":[{"file_hash":[{"type":2,"value":"` + md5base64 + `"}]}]}` + "\n" +
			`{"location":"gs://bucket/bar.txt","file_hash":[{"file_hash":[{"type":2,"value":"` + md5base64 + `"}]}]}` + "\n",
	}, {
		name:    "SuccessEmptyFile",
		results: []*pb.ArtifactResult{},
	}}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := newMockRunner(t, tc.name)
			fs := afero.NewMemMapFs()
			gsutilHelper := New(r, fs)

			if err := gsutilHelper.createArtifactsManifest(manifestPath, tc.results); err == nil && tc.wantErr {
				t.Error("got gsutilHelper.writeArtifactsManifest() = nil, want error")
			} else if err != nil && !tc.wantErr {
				t.Errorf("got gsutilHelper.writeArtifactsManifest() = %v, want nil", err)
			}

			gotManifest, err := afero.ReadFile(fs, manifestPath)
			if err != nil {
				t.Fatalf("afero.ReadFile(%s): err = %+v", manifestPath, err)
			}
			if string(gotManifest) != tc.wantManifest {
				t.Errorf("got manifest = \n%q\n want manifest =\n%q", gotManifest, tc.wantManifest)
			}
		})
	}
}

func TestAdjustDest(t *testing.T) {
	testCases := []struct {
		name     string
		src      string
		dest     string
		wantDest string
	}{{
		name:     "NoLeadDir",
		src:      "test.xml",
		dest:     "gs://bucket/",
		wantDest: "gs://bucket/",
	}, {
		name:     "LeadDir",
		src:      "some/path/test.xml",
		dest:     "gs://bucket/",
		wantDest: "gs://bucket/some/path/",
	}, {
		name:     "LeadDirBucketDir",
		src:      "/some/path/test.xml",
		dest:     "gs://bucket/some/path/",
		wantDest: "gs://bucket/some/path/some/path/",
	}, {
		name:     "SlashLeadDir",
		src:      "/some/path/test.xml",
		dest:     "gs://bucket/",
		wantDest: "gs://bucket/some/path/",
	}, {
		name:     "DotSlashNoLeadDir",
		src:      "./test.xml",
		dest:     "gs://bucket/",
		wantDest: "gs://bucket/",
	}, {
		name:     "DotSlashLeadDir",
		src:      "./some/path/test.xml",
		dest:     "gs://bucket/",
		wantDest: "gs://bucket/some/path/",
	}}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if gotDest := adjustDest(tc.src, tc.dest); gotDest != tc.wantDest {
				t.Errorf("got %q, want %q", gotDest, tc.wantDest)
			}
		})
	}
}
