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

package metadata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestOauth2(t *testing.T) {
	tok := Token{
		AccessToken: "yadda-yadda-yadda",
		Expiry:      time.Now(),
		Email:       "bogus@not-a-domain.nowhere",
	}

	want := oauth2.Token{
		AccessToken: tok.AccessToken,
		Expiry:      tok.Expiry,
		TokenType:   "Bearer",
	}

	got := tok.Oauth2()
	if want != *got {
		t.Errorf("want %v; got %v", want, got)
	}
}

func TestGetAddress(t *testing.T) {
	for _, tc := range []struct {
		local bool
		want  string
	}{
		{true, localMetadata},
		{false, "http://" + metadataHostedIP},
	} {
		u := RealUpdater{tc.local}
		if got := u.getAddress(); got != tc.want {
			t.Errorf("IsLocal==%t: got %q, want %q", tc.local, got, tc.want)
		}
	}
}

func TestReady(t *testing.T) {
	cases := []struct {
		readyStatusCode int
		timeout         time.Duration
		want            bool
		name            string
	}{
		{http.StatusOK, time.Second, true, "happy case"},
		{http.StatusOK, -1 * time.Second, false, "timed out"},
		{http.StatusBadRequest, time.Second, false, "bad request"},
	}

	for _, tc := range cases {
		wasInfoHost := googleTokenInfoHost
		wasMetadataIP := metadataHostedIP
		defer func() {
			googleTokenInfoHost = wasInfoHost
			metadataHostedIP = wasMetadataIP
		}()
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if want, got := headerVal, r.Header.Get(headerKey); want != got {
					t.Errorf("Missing %q header: want %q, got %q", headerKey, want, got)
				}
				if r.Method != http.MethodGet {
					t.Fatalf("want %q, got %q", http.MethodGet, r.Method)
				}
				const wantEndpoint = "/ready"
				if r.URL.Path != wantEndpoint {
					t.Fatalf("want %q endpoint, got %q endpoint", wantEndpoint, r.URL.Path)
				}
				w.WriteHeader(tc.readyStatusCode)
				return
			}))
			defer s.Close()
			googleTokenInfoHost = s.URL
			metadataHostedIP = s.URL

			u := RealUpdater{false}
			ctx, cancel := context.WithTimeout(context.Background(), tc.timeout)
			defer cancel()
			got := u.Ready(ctx)
			if tc.want != got {
				t.Errorf("got %t, want %t", got, tc.want)
			}
		})
	}
}

func testServer(t *testing.T,
	tokenStatusCode int, // response to a SetToken
	scopesStatusCode int, scopesBody string, // response to a getScopes
	buildStatusCode int, // response to a POST "/build"
) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if want, got := headerVal, r.Header.Get(headerKey); want != got {
			t.Errorf("Missing %q header: want %q, got %q", headerKey, want, got)
		}
		if r.Method == http.MethodPost {
			switch r.URL.Path {
			case "/token":
				w.WriteHeader(tokenStatusCode)
				return

			case "/oauth2/v3/tokeninfo":
				w.WriteHeader(scopesStatusCode)
				w.Write([]byte(scopesBody))
				return

			case "/build":
				w.WriteHeader(buildStatusCode)
				return
			}

			// Anything else is an invalid request.
			t.Errorf("Unexpected %q request to %q", r.Method, r.URL)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
	}))
}

func TestSetToken(t *testing.T) {
	ctx := context.Background()
	wasInfoHost := googleTokenInfoHost
	wasMetadataIP := metadataHostedIP
	defer func() {
		googleTokenInfoHost = wasInfoHost
		metadataHostedIP = wasMetadataIP
	}()

	cases := []struct {
		tokenStatusCode, scopesStatusCode int
		scopesBody                        string
		wantError                         bool
		name                              string
	}{
		{http.StatusOK, http.StatusOK, "{}", false, "happy case"},
		{http.StatusOK, http.StatusOK, "", true, "no scopes"},
		{http.StatusBadRequest, http.StatusOK, "{}", true, "bad token request"},
		{http.StatusOK, http.StatusBadRequest, "{}", true, "bad scopes request"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(t, tc.tokenStatusCode, tc.scopesStatusCode, tc.scopesBody, 0)
			defer s.Close()
			googleTokenInfoHost = s.URL
			metadataHostedIP = s.URL

			u := RealUpdater{false}
			got := u.SetToken(ctx, &Token{})
			if got == nil && tc.wantError || got != nil && !tc.wantError {
				t.Errorf("got %v, wantError==%t", got, tc.wantError)
			}
		})
	}
}

func TestGetScopes(t *testing.T) {
	ctx := context.Background()
	wasInfoHost := googleTokenInfoHost
	wasMetadataIP := metadataHostedIP
	defer func() {
		googleTokenInfoHost = wasInfoHost
		metadataHostedIP = wasMetadataIP
	}()
	cases := []struct {
		statusCode int
		body       string
		wantScopes []string
		wantError  bool
		name       string
	}{
		{http.StatusOK, "{}", []string{""}, false, "happy case no scopes"},
		{http.StatusOK, `{"scope": "this"}`, []string{"this"}, false, "happy case one scope"},
		{http.StatusOK, `{"scope": "this these that those"}`, []string{"this", "these", "that", "those"}, false, "happy case many scopes"},
		{http.StatusOK, "", nil, true, "empty json"},
		{http.StatusBadRequest, "{}", nil, true, "bad request"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(t, 0, tc.statusCode, tc.body, 0)
			defer s.Close()
			googleTokenInfoHost = s.URL
			metadataHostedIP = s.URL

			gotScopes, gotErr := getScopes(ctx, "token string")
			if gotErr == nil && tc.wantError || gotErr != nil && !tc.wantError {
				t.Errorf("got %v, wantError==%t", gotErr, tc.wantError)
			}
			if !reflect.DeepEqual(gotScopes, tc.wantScopes) {
				t.Errorf("got scopes %v, want %v", gotScopes, tc.wantScopes)
			}
		})
	}
}

func TestProjectInfo(t *testing.T) {
	ctx := context.Background()
	wasInfoHost := googleTokenInfoHost
	wasMetadataIP := metadataHostedIP
	defer func() {
		googleTokenInfoHost = wasInfoHost
		metadataHostedIP = wasMetadataIP
	}()
	cases := []struct {
		statusCode int
		wantError  bool
		name       string
	}{
		{http.StatusOK, false, "happy case"},
		{http.StatusBadRequest, true, "bad request"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(t, 0, 0, "", tc.statusCode)
			defer s.Close()
			googleTokenInfoHost = s.URL
			metadataHostedIP = s.URL

			u := RealUpdater{false}
			got := u.SetProjectInfo(ctx, ProjectInfo{})
			if got == nil && tc.wantError || got != nil && !tc.wantError {
				t.Errorf("got %v, wantError==%t", got, tc.wantError)
			}
		})
	}
}
