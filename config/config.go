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

// Package config provides methods to read a cloudbuild file and convert it to
// a build proto.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"

	"github.com/golang/protobuf/jsonpb"

	"gopkg.in/yaml.v2"

	pb "google.golang.org/genproto/googleapis/devtools/cloudbuild/v1"
)

// Load loads a config file and transforms it to a build proto.
func Load(path string) (*pb.Build, error) {
	configContent, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("Unable to read config file: %v", err)
	}
	return unmarshalBuildTemplate(configContent)
}

// YAML unmarshalling produces a map[string]interface{} where the value might
// be a map[interface{}]interface{}, or a []interface{} where values might be a
// map[interface{}]interface{}, which json.Marshal does not support.
//
// So we have to go through the map and change any map[interface{}]interface{}
// we find into a map[string]interface{}, which JSON decoding supports.
func fix(in interface{}) interface{} {
	switch in.(type) {
	case map[interface{}]interface{}:
		// Create a new map[string]interface{} and fill it with fixed keys.
		cp := map[string]interface{}{}
		for k, v := range in.(map[interface{}]interface{}) {
			cp[fmt.Sprintf("%s", k)] = v
		}
		// Now fix the map[string]interface{} to fix the values.
		return fix(cp)
	case map[string]interface{}:
		// Fix each value in the map.
		sm := in.(map[string]interface{})
		for k, v := range sm {
			sm[k] = fix(v)
		}
		return sm
	case []interface{}:
		// Fix each element in the slice.
		s := in.([]interface{})
		for i, v := range s {
			s[i] = fix(v)
		}
		return s
	default:
		// Value doesn't need to be fixed. If this is not a supported type, JSON
		// encoding will fail.
		return in
	}
}

func unmarshalBuildTemplate(content []byte) (*pb.Build, error) {
	var m map[string]interface{}
	if err := yaml.Unmarshal(content, &m); err != nil {
		return nil, err
	}
	fix(m)
	jb, err := json.Marshal(&m)
	if err != nil {
		return nil, err
	}

	// This returns an error if the JSON includes unknown fields. The error will
	// reference the *first* field it finds that isn't in the proto, not an
	// exhaustive list of all unknown fields.
	var b pb.Build
	if err := jsonpb.Unmarshal(bytes.NewReader(jb), &b); err != nil {
		if err.Error() == "json: cannot unmarshal string into Go value of type []json.RawMessage" {
			return &b, errors.New("repeated string fields containing one element must be specified like ['foo'], not 'foo'")
		}
		return &b, err
	}
	return &b, err
}
