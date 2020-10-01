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

package subst

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/GoogleCloudPlatform/cloud-build-local/common"
	"google3/third_party/golang/drone/envsubst/envsubst"
	"google3/third_party/golang/drone/envsubst/parse/parse"
)

const (
	// set maxSubstitutionSize to 1 greater than what Cloud Build allows
	// so the user will see a rejected build.
	maxSubstitutionSize = commonconst.MaxSubstValueLength + 1
)

var (
	ErrSubstitutionCycle = errors.New("cycle in evaluating substitutions")
	noBracesRE           = regexp.MustCompile(`^\$[A-Z0-9_]+`)
)

type substOrErr struct {
	original    string
	value       string
	err         error
	isTemporary bool
}

// A preProcessor is a function applied to a substitution before
// any Bash style string manipulation is applied.
type preProcessor func(string) (string, error)

func init() {
	envsubst.MaxLength = maxSubstitutionSize
}

// EvalSubstitutions applies bash style substitutions to the provided map
// and returns a new copy with all variables evaluated.
// Any variable whose substitution has a syntax error or couldn't be found
// is evaluated as the empty string.
func EvalSubstitutions(subst map[string]string, preProcessors ...preProcessor) (map[string]string, error) {
	localSubst := make(map[string]*substOrErr)
	for k, v := range subst {
		localSubst[k] = &substOrErr{original: v}
		// drone/envsubst expects syntax of the form ${FOO}, but we want to
		// support $FOO (no braces), so do a simple check to see whether a
		// value starts with "$" but not "${" and, in that case, wrap it
		// in ${...} to process it uniformly with the rest of the substitutions.
		if noBracesRE.MatchString(v) {
			localSubst[k].value = "${" + v[1:] + "}"
		} else {
			localSubst[k].value = v
		}
	}
	// Now that all of the bash env cleanup is done, iterate over this map.
	for k, _ := range subst {
		EvalSubstitution(k, localSubst, map[string]bool{}, preProcessors...)
	}

	result := make(map[string]string)
	substOrErrors := []string{}
	for k, substOrErr := range localSubst {
		if substOrErr.err != nil {
			substOrErrors = append(substOrErrors, fmt.Sprintf("[%v -> %v]: %v", k, substOrErr.original, substOrErr.err))
		}
		if !substOrErr.isTemporary {
			result[k] = substOrErr.value
		}
	}
	if len(substOrErrors) > 0 {
		return nil, errors.New(strings.Join(substOrErrors, ", "))
	}
	return result, nil
}

// EvalSubstitution evaluates the value in subst referred to by key "root". It does
// this by first recursively evaluating any variables that "root" depends
// on and only then evaluating "root".
func EvalSubstitution(root string, subst map[string]*substOrErr, seen map[string]bool, preProcessors ...preProcessor) {
	defer func() {
		if r := recover(); r != nil {
			subst[root].err = fmt.Errorf("unexpected error in processing substitution: %s+v", r)
			return
		}
	}()
	if _, ok := subst[root]; !ok {
		// Treat undefined vars as the empty string just like Bash does.
		subst[root] = &substOrErr{value: "", isTemporary: true}
	}
	// Apply all the preProcessors on the value.
	for _, preProcessor := range preProcessors {
		v, err := preProcessor(subst[root].value)
		if err != nil {
			subst[root].err = err
			return
		}
		subst[root].value = v
	}
	// If subst[root] = "${BAR^^}, then we need to evaluate the "BAR"
	// in ${BAR^^} before evaluating subst[root].
	parsedTree, err := parse.Parse(subst[root].value)
	if err != nil {
		subst[root].err = err
		return
	}
	seen[root] = true
	defer func() {
		seen[root] = false
	}()
	switch v := parsedTree.Root.(type) {
	case *parse.TextNode:
		// We have found a value, so no work to be done.
		// e.g. subst[root].value = "master"
		return
	case *parse.FuncNode:
		// e.g. subst[root].value = "${BAR:-${BAZ}}", in that case, the args need
		// to be evaluated before "subst[root]"
		if seen[v.Param] {
			subst[v.Param].err = ErrSubstitutionCycle
			return
		}
		// Evaluate the "args" (e.g. "BAZ")
		evalListNode(v.Args, subst, seen, preProcessors...)
		// Now evaluate v.Param (e.g. "BAR").
		EvalSubstitution(v.Param, subst, seen, preProcessors...)
	case *parse.ListNode:
		evalListNode(v.Nodes, subst, seen, preProcessors...)
	}
	// Now that subst[BAR] has been evaluated (possibly to the empty string),
	// evaluate subst[root] (e.g. "${BAR^^}").
	if modifiedValue, err := envsubst.Eval(subst[root].value, func(s string) string {
		if _, ok := subst[s]; ok {
			return subst[s].value
		}
		return ""
	}); err != nil {
		subst[root].err = err
	} else {
		subst[root].value = modifiedValue
	}
}

// evalListNode iterates over each node and evaluates them in order.
func evalListNode(nodes []parse.Node, subst map[string]*substOrErr, seen map[string]bool, preProcessors ...preProcessor) {
	for _, node := range nodes {
		switch v := node.(type) {
		case *parse.TextNode:
			// no work to be done.
			continue
		case *parse.FuncNode:
			if seen[v.Param] {
				subst[v.Param].err = ErrSubstitutionCycle
				continue
			}
			seen[v.Param] = true
			defer func() {
				seen[v.Param] = false
			}()
			EvalSubstitution(v.Param, subst, seen, preProcessors...)
			continue
		case *parse.ListNode:
			evalListNode(v.Nodes, subst, seen, preProcessors...)
		}
	}
}
