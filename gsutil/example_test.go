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

package gsutil_test

import (
	"fmt"

	"github.com/GoogleCloudPlatform/container-builder-local/gsutil"
	"github.com/GoogleCloudPlatform/container-builder-local/runner"
)

func ExampleBucketExists() {
	r := &runner.RealRunner{}
	gsutilHelper := gsutil.New(r)

	// The URL of the Storage Bucket present in your Google Cloud project.
	bucket := "gs://some-bucket-name"

	exists := gsutilHelper.BucketExists(bucket)

	if !exists {
		fmt.Printf("Bucket %q does not exist.", bucket)
	}
}
