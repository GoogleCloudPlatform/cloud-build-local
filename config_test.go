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

package config

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	cb "google.golang.org/api/cloudbuild/v1"
	// 		cb "vendor/cloudbuild"
)

func TestLoad(t *testing.T) {

	testCases := []struct {
		desc    string
		content string
		want    *cb.Build
		wantErr bool
	}{{
		desc: "happy case",
		content: `
steps:
- name: "gcr.io/my-project/my-builder"
  args:
  - "foo"
  - "bar"
 `,
		want: &cb.Build{
			Steps: []*cb.BuildStep{{
				Name: "gcr.io/my-project/my-builder",
				Args: []string{"foo", "bar"},
			}},
		},
	}, {
		desc: "happy case",
		content: `
steps:
- name: 'gcr.io/cloud-builders/docker'
  args: ["build", "-t", "gcr.io/$PROJECT_ID/test:latest", "."]

images: ['gcr.io/$PROJECT_ID/test:latest']
`,
		want: &cb.Build{
			Steps: []*cb.BuildStep{{
				Name: "gcr.io/cloud-builders/docker",
				Args: []string{"build", "-t", "gcr.io/$PROJECT_ID/test:latest", "."},
			}},
			Images: []string{"gcr.io/$PROJECT_ID/test:latest"},
		},
	}, {
		desc: "unknown field",
		content: `
steps:
- name: 'gcr.io/cloud-builders/docker'
  args: ["build", "-t", "gcr.io/$PROJECT_ID/test:latest", "."]
foo: "bar"
`,
		wantErr: true,
	}}

	for _, tc := range testCases {
		tmpDir, err := ioutil.TempDir("", "")
		defer os.RemoveAll(tmpDir) // clean up
		if err != nil {
			t.Fatalf("problem making temp dir: %v", err)
		}
		fname := filepath.Join(tmpDir, "some_file")
		if err := ioutil.WriteFile(fname, []byte(tc.content), 0666); err != nil {
			t.Fatalf("Error writing file: %v", err)
		}

		got, err := Load(fname)
		if err != nil && !tc.wantErr {
			t.Errorf("%s: Load failed: %v", tc.desc, err)
		} else if err == nil && tc.wantErr {
			t.Errorf("%s: Load should have failed", tc.desc)
		}
		if !tc.wantErr && !reflect.DeepEqual(tc.want, got) {
			t.Errorf("%s: Load failed to translate file in build proto: want %+v, got %+v", tc.desc, tc.want, got)
		}
	}
}
