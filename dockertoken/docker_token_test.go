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

package dockertoken

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
)

type mockRunner struct {
	mu       sync.Mutex
	t        *testing.T
	commands []string
}

func newMockRunner(t *testing.T) *mockRunner {
	return &mockRunner{
		t: t,
	}
}

func (r *mockRunner) Run(ctx context.Context, args []string, in io.Reader, out, err io.Writer) error {
	r.mu.Lock()
	r.commands = append(r.commands, strings.Join(args, " "))
	r.mu.Unlock()
	return nil
}

func TestNew(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := newMockRunner(t)
	_, err := New(ctx, r)
	if err != nil {
		t.Fatal(err)
	}

	got := strings.Join(r.commands, "\n")
	want := "docker run --name docker_token_container --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock -itd busybox ash"
	if got != want {
		t.Errorf("CreateDockerTokenContainer did not run expected command; \n got %q \n want %q", got, want)
	}
}

func TestClose(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := newMockRunner(t)
	d, err := New(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	err = d.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}

	got := r.commands[1]
	want := "docker rm -f docker_token_container"
	if got != want {
		t.Errorf("Close did not run expected command; \n got %q \n want %q", got, want)
	}
}

// TestDockerAccessToken tests the commands executed when setting and
// updating Docker access tokens.
func TestDockerAccessToken(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := newMockRunner(t)
	d, err := New(ctx, r)
	if err != nil {
		t.Fatal(err)
	}

	if err := d.Set(ctx, "FIRST"); err != nil {
		t.Errorf("SetDockerAccessToken: %v", err)
	}
	firstToken := base64.StdEncoding.EncodeToString([]byte("oauth2accesstoken:FIRST"))

	if err := d.Set(ctx, "SECOND"); err != nil {
		t.Errorf("SetDockerAccessToken: %v", err)
	}
	secondToken := base64.StdEncoding.EncodeToString([]byte("oauth2accesstoken:SECOND"))

	got := strings.Join(r.commands, "\n")
	want := strings.Join([]string{
		"docker run --name docker_token_container --volume homevol:/builder/home --env HOME=/builder/home --volume /var/run/docker.sock:/var/run/docker.sock -itd busybox ash",
		fmt.Sprint(
			`docker exec `, dockerTokenContainerName, " ", shell,
			` -c mkdir -p ~/.docker/ && cat << EOF > ~/.docker/config.json
{
  "auths": {
    "https://asia-docker.pkg.dev": {
      "auth": "`, firstToken, `"
    },
    "https://asia-east1-docker.pkg.dev": {
      "auth": "`, firstToken, `"
    },
    "https://asia-east2-docker.pkg.dev": {
      "auth": "`, firstToken, `"
    },
    "https://asia-northeast1-docker.pkg.dev": {
      "auth": "`, firstToken, `"
    },
    "https://asia-northeast2-docker.pkg.dev": {
      "auth": "`, firstToken, `"
    },
    "https://asia-south1-docker.pkg.dev": {
      "auth": "`, firstToken, `"
    },
    "https://asia-southeast1-docker.pkg.dev": {
      "auth": "`, firstToken, `"
    },
    "https://asia.gcr.io": {
      "auth": "`, firstToken, `"
    },
    "https://australia-southeast1-docker.pkg.dev": {
      "auth": "`, firstToken, `"
    },
    "https://b.gcr.io": {
      "auth": "`, firstToken, `"
    },
    "https://bucket.gcr.io": {
      "auth": "`, firstToken, `"
    },
    "https://docker.pkg.dev": {
      "auth": "`, firstToken, `"
    },
    "https://eu.gcr.io": {
      "auth": "`, firstToken, `"
    },
    "https://europe-docker.pkg.dev": {
      "auth": "`, firstToken, `"
    },
    "https://europe-north1-docker.pkg.dev": {
      "auth": "`, firstToken, `"
    },
    "https://europe-west1-docker.pkg.dev": {
      "auth": "`, firstToken, `"
    },
    "https://europe-west2-docker.pkg.dev": {
      "auth": "`, firstToken, `"
    },
    "https://europe-west3-docker.pkg.dev": {
      "auth": "`, firstToken, `"
    },
    "https://europe-west4-docker.pkg.dev": {
      "auth": "`, firstToken, `"
    },
    "https://europe-west6-docker.pkg.dev": {
      "auth": "`, firstToken, `"
    },
    "https://gcr-staging.sandbox.google.com": {
      "auth": "`, firstToken, `"
    },
    "https://gcr.io": {
      "auth": "`, firstToken, `"
    },
    "https://k8s.gcr.io": {
      "auth": "`, firstToken, `"
    },
    "https://marketplace.gcr.io": {
      "auth": "`, firstToken, `"
    },
    "https://northamerica-northeast1-docker.pkg.dev": {
      "auth": "`, firstToken, `"
    },
    "https://southamerica-east1-docker.pkg.dev": {
      "auth": "`, firstToken, `"
    },
    "https://us-central1-docker.pkg.dev": {
      "auth": "`, firstToken, `"
    },
    "https://us-docker.pkg.dev": {
      "auth": "`, firstToken, `"
    },
    "https://us-east1-docker.pkg.dev": {
      "auth": "`, firstToken, `"
    },
    "https://us-east4-docker.pkg.dev": {
      "auth": "`, firstToken, `"
    },
    "https://us-west1-docker.pkg.dev": {
      "auth": "`, firstToken, `"
    },
    "https://us-west2-docker.pkg.dev": {
      "auth": "`, firstToken, `"
    },
    "https://us.gcr.io": {
      "auth": "`, firstToken, `"
    }
  }
}
EOF`), fmt.Sprint(
			`docker exec `, dockerTokenContainerName, " ", shell,
			` -c mkdir -p ~/.docker/ && cat << EOF > ~/.docker/config.json
{
  "auths": {
    "https://asia-docker.pkg.dev": {
      "auth": "`, secondToken, `"
    },
    "https://asia-east1-docker.pkg.dev": {
      "auth": "`, secondToken, `"
    },
    "https://asia-east2-docker.pkg.dev": {
      "auth": "`, secondToken, `"
    },
    "https://asia-northeast1-docker.pkg.dev": {
      "auth": "`, secondToken, `"
    },
    "https://asia-northeast2-docker.pkg.dev": {
      "auth": "`, secondToken, `"
    },
    "https://asia-south1-docker.pkg.dev": {
      "auth": "`, secondToken, `"
    },
    "https://asia-southeast1-docker.pkg.dev": {
      "auth": "`, secondToken, `"
    },
    "https://asia.gcr.io": {
      "auth": "`, secondToken, `"
    },
    "https://australia-southeast1-docker.pkg.dev": {
      "auth": "`, secondToken, `"
    },
    "https://b.gcr.io": {
      "auth": "`, secondToken, `"
    },
    "https://bucket.gcr.io": {
      "auth": "`, secondToken, `"
    },
    "https://docker.pkg.dev": {
      "auth": "`, secondToken, `"
    },
    "https://eu.gcr.io": {
      "auth": "`, secondToken, `"
    },
    "https://europe-docker.pkg.dev": {
      "auth": "`, secondToken, `"
    },
    "https://europe-north1-docker.pkg.dev": {
      "auth": "`, secondToken, `"
    },
    "https://europe-west1-docker.pkg.dev": {
      "auth": "`, secondToken, `"
    },
    "https://europe-west2-docker.pkg.dev": {
      "auth": "`, secondToken, `"
    },
    "https://europe-west3-docker.pkg.dev": {
      "auth": "`, secondToken, `"
    },
    "https://europe-west4-docker.pkg.dev": {
      "auth": "`, secondToken, `"
    },
    "https://europe-west6-docker.pkg.dev": {
      "auth": "`, secondToken, `"
    },
    "https://gcr-staging.sandbox.google.com": {
      "auth": "`, secondToken, `"
    },
    "https://gcr.io": {
      "auth": "`, secondToken, `"
    },
    "https://k8s.gcr.io": {
      "auth": "`, secondToken, `"
    },
    "https://marketplace.gcr.io": {
      "auth": "`, secondToken, `"
    },
    "https://northamerica-northeast1-docker.pkg.dev": {
      "auth": "`, secondToken, `"
    },
    "https://southamerica-east1-docker.pkg.dev": {
      "auth": "`, secondToken, `"
    },
    "https://us-central1-docker.pkg.dev": {
      "auth": "`, secondToken, `"
    },
    "https://us-docker.pkg.dev": {
      "auth": "`, secondToken, `"
    },
    "https://us-east1-docker.pkg.dev": {
      "auth": "`, secondToken, `"
    },
    "https://us-east4-docker.pkg.dev": {
      "auth": "`, secondToken, `"
    },
    "https://us-west1-docker.pkg.dev": {
      "auth": "`, secondToken, `"
    },
    "https://us-west2-docker.pkg.dev": {
      "auth": "`, secondToken, `"
    },
    "https://us.gcr.io": {
      "auth": "`, secondToken, `"
    }
  }
}
EOF`)}, "\n")
	if got != want {
		t.Errorf("Commands didn't match!\n===Got:\n%s\n===Want:\n%s", got, want)
	}
	// if diff := cmp.Diff(got, want); diff != "" {
	//      t.Errorf("Commands didn't match: %s", diff)
	//   }
}
