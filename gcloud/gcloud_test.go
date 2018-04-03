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

package gcloud

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

type mockRunner struct {
	mu           sync.Mutex
	testCaseName string
	commands     []string
	projectID    string
	configHelper string
}

const (
	projectID  = "my-project-id"
	projectNum = 1234
	// validConfig is what `gcloud config config-helper --format=json` spits out
	validConfig = `{
  "configuration": {
    "active_configuration": "default",
    "properties": {
      "compute": {
        "region": "us-central1",
        "zone": "us-central1-f"
      },
      "core": {
        "account": "bogus@not-a-domain.nowhere",
        "disable_usage_reporting": "False",
        "project": "my-project-id"
      }
    }
  },
  "credential": {
    "access_token": "my-token",
    "token_expiry": "2100-08-24T23:46:12Z"
  },
  "sentinels": {
    "config_sentinel": "/home/someone/.config/gcloud/config_sentinel"
  }
}
`
	cfgNoToken = `{
  "configuration": {
    "properties": {
      "core": {
        "account": "bogus@not-a-domain.nowhere"
      }
    }
	}
}`
	cfgNoAcct = `{
  "credential": {
    "access_token": "my-token"
  }
}`
	cfgNoExpiration = `{
  "configuration": {
    "properties": {
      "core": {
        "account": "bogus@not-a-domain.nowhere"
      }
    }
  },
  "credential": {
    "access_token": "my-token"
  }
}`
	cfgExpired = `{
  "configuration": {
    "properties": {
      "core": {
        "account": "bogus@not-a-domain.nowhere"
      }
    }
  },
  "credential": {
    "access_token": "my-token",
    "token_expiry": "1999-01-01T00:00:00Z"
  }
}
`
)

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

func (r *mockRunner) Run(ctx context.Context, args []string, in io.Reader, out, err io.Writer, _ string) error {
	r.mu.Lock()
	r.commands = append(r.commands, strings.Join(args, " "))
	r.mu.Unlock()

	if startsWith(args, "gcloud", "config", "list") {
		fmt.Fprintln(out, r.projectID)
	} else if startsWith(args, "gcloud", "projects", "describe") && r.projectID == projectID {
		fmt.Fprintln(out, projectNum)
	} else if startsWith(args, "gcloud", "config", "config-helper", "--format=json") {
		fmt.Fprintln(out, r.configHelper)
	}

	return nil
}

func (r *mockRunner) MkdirAll(dir string) error {
	return nil
}

func (r *mockRunner) WriteFile(path, contents string) error {
	return nil
}

func (r *mockRunner) Clean() error {
	return nil
}

func TestAccessToken(t *testing.T) {
	ctx := context.Background()
	// Happy case.
	r := &mockRunner{configHelper: validConfig}
	token, err := AccessToken(ctx, r)
	if err != nil {
		t.Errorf("AccessToken failed: %v", err)
	}
	if token.AccessToken != "my-token" {
		t.Errorf("AccessToken failed returning the token; got %q, want %q", token.AccessToken, "my-token")
	}
	if token.Email != "bogus@not-a-domain.nowhere" {
		t.Errorf("AccessToken failed returning the email; got %q, want %q", token.Email, "bogus@not-a-domain.nowhere")
	}
	got := strings.Join(r.commands, "\n")
	want := "gcloud config config-helper --format=json"
	if got != want {
		t.Errorf("Commands didn't match!\n===Want:\n%s\n===Got:\n%s", want, got)
	}

	// We'll look for this error below...
	_, errParseTime := time.Parse(time.RFC3339, "")

	// Unhappy cases
	for _, tc := range []struct {
		desc string
		json string
		want error
	}{{
		desc: "empty json",
		json: "{}",
		want: errTokenNotFound,
	}, {
		desc: "no token",
		json: cfgNoToken,
		want: errTokenNotFound,
	}, {
		desc: "no account",
		json: cfgNoAcct,
		want: errAcctNotFound,
	}, {
		desc: "no expiration",
		json: cfgNoExpiration,
		want: errParseTime,
	}, {
		desc: "expired",
		json: cfgExpired,
		want: errTokenExpired,
	}} {
		r.configHelper = tc.json
		token, err = AccessToken(ctx, r)
		if err.Error() != tc.want.Error() {
			t.Errorf("%s: got %v; want %v", tc.desc, err, tc.want)
		}
		if token != nil {
			t.Errorf("%s: got unexpected token %v", tc.desc, token)
		}
	}
}

func TestProjectInfo(t *testing.T) {
	r := &mockRunner{projectID: projectID}
	ctx := context.Background()
	projectInfo, err := ProjectInfo(ctx, r)
	if err != nil {
		t.Errorf("ProjectInfo failed: %v", err)
	}
	if projectInfo.ProjectID != projectID {
		t.Errorf("ProjectInfo failed returning the projectID; got %s, want %s", projectInfo.ProjectID, projectID)
	}
	if projectInfo.ProjectNum != projectNum {
		t.Errorf("ProjectInfo failed returning the projectNum; got %d, want %d", projectInfo.ProjectNum, projectNum)
	}

	got := strings.Join(r.commands, "\n")
	want := strings.Join([]string{`gcloud config list --format value(core.project)`,
		fmt.Sprintf(`gcloud projects describe %s --format value(projectNumber)`, projectID)}, "\n")
	if got != want {
		t.Errorf("Commands didn't match!\n===Want:\n%s\n===Got:\n%s", want, got)
	}
}

func TestProjectInfoError(t *testing.T) {
	r := &mockRunner{}
	ctx := context.Background()
	_, err := ProjectInfo(ctx, r)
	if err == nil {
		t.Errorf("ProjectInfo should fail when no projectId set in gcloud")
	}

	r.projectID = "some-other-project"
	_, err = ProjectInfo(ctx, r)
	if err == nil {
		t.Errorf("ProjectInfo should fail when no projectNum available from gcloud")
	}
}
