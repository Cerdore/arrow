// Licensed to the Apache Software Foundation (ASF) under one
// or more contributor license agreements.  See the NOTICE file
// distributed with this work for additional information
// regarding copyright ownership.  The ASF licenses this file
// to you under the Apache License, Version 2.0 (the
// "License"); you may not use this file except in compliance
// with the License.  You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/format"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/apache/arrow/go/v17/internal/json"
)

const Ext = ".tmpl"

type pathSpec struct {
	in, out string
}

func (p *pathSpec) String() string { return p.in + " → " + p.out }
func (p *pathSpec) IsGoFile() bool { return filepath.Ext(p.out) == ".go" }

func parsePath(path string) (string, string) {
	p := strings.IndexByte(path, '=')
	if p == -1 {
		if filepath.Ext(path) != Ext {
			errExit("template file '%s' must have .tmpl extension", path)
		}
		return path, path[:len(path)-len(Ext)]
	}

	return path[:p], path[p+1:]
}

type data struct {
	In interface{}
	D  listValue
}

func errExit(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, format, a...)
	fmt.Fprintln(os.Stderr)
	os.Exit(1)
}

type listValue map[string]string

func (l listValue) String() string {
	res := make([]string, 0, len(l))
	for k, v := range l {
		res = append(res, fmt.Sprintf("%s=%s", k, v))
	}
	return strings.Join(res, ", ")
}

func (l listValue) Set(v string) error {
	nv := strings.Split(v, "=")
	if len(nv) != 2 {
		return fmt.Errorf("expected NAME=VALUE, got %s", v)
	}
	l[nv[0]] = nv[1]
	return nil
}

func main() {
	var (
		dataArg = flag.String("data", "", "input JSON data")
		gi      = flag.Bool("i", false, "run goimports")
		in      = &data{D: make(listValue)}
	)

	flag.Var(&in.D, "d", "-d NAME=VALUE")

	flag.Parse()
	if *dataArg == "" {
		errExit("data option is required")
	}

	if *gi {
		if _, err := exec.LookPath("goimports"); err != nil {
			errExit("failed to find goimports: %s", err.Error())
		}
		formatter = formatSource
	} else {
		formatter = format.Source
	}

	paths := flag.Args()
	if len(paths) == 0 {
		errExit("no tmpl files specified")
	}

	specs := make([]pathSpec, len(paths))
	for i, p := range paths {
		in, out := parsePath(p)
		specs[i] = pathSpec{in: in, out: out}
	}

	in.In = readData(*dataArg)
	process(in, specs)
}

func mustReadAll(path string) []byte {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		errExit(err.Error())
	}

	return data
}

func readData(path string) interface{} {
	data := mustReadAll(path)
	var v interface{}
	if err := json.Unmarshal(StripComments(data), &v); err != nil {
		errExit("invalid JSON data: %s", err.Error())
	}
	return v
}

func fileMode(path string) os.FileMode {
	stat, err := os.Stat(path)
	if err != nil {
		errExit(err.Error())
	}
	return stat.Mode()
}

var funcs = template.FuncMap{
	"lower": strings.ToLower,
	"upper": strings.ToUpper,
}

func process(data interface{}, specs []pathSpec) {
	for _, spec := range specs {
		var (
			t   *template.Template
			err error
		)
		t, err = template.New("gen").Funcs(funcs).Parse(string(mustReadAll(spec.in)))
		if err != nil {
			errExit("error processing template '%s': %s", spec.in, err.Error())
		}

		var buf bytes.Buffer
		if spec.IsGoFile() {
			// preamble
			fmt.Fprintf(&buf, "// Code generated by %s. DO NOT EDIT.\n", spec.in)
			fmt.Fprintln(&buf)
		}
		err = t.Execute(&buf, data)
		if err != nil {
			errExit("error executing template '%s': %s", spec.in, err.Error())
		}

		generated := buf.Bytes()
		if spec.IsGoFile() {
			generated, err = formatter(generated)
			if err != nil {
				errExit("error formatting '%s': %s", spec.in, err.Error())
			}
		}

		os.WriteFile(spec.out, generated, fileMode(spec.in))
	}
}

var (
	formatter func([]byte) ([]byte, error)
)

func formatSource(in []byte) ([]byte, error) {
	r := bytes.NewReader(in)
	cmd := exec.Command("goimports")
	cmd.Stdin = r
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("error running goimports: %s", string(ee.Stderr))
		}
		return nil, fmt.Errorf("error running goimports: %s", string(out))
	}

	return out, nil
}

func StripComments(raw []byte) []byte {
	var (
		quoted, esc bool
		comment     bool
	)

	buf := bytes.Buffer{}

	for i := 0; i < len(raw); i++ {
		b := raw[i]

		if comment {
			switch b {
			case '/':
				comment = false
				j := bytes.IndexByte(raw[i+1:], '\n')
				if j == -1 {
					i = len(raw)
				} else {
					i += j // keep new line
				}
			case '*':
				j := bytes.Index(raw[i+1:], []byte("*/"))
				if j == -1 {
					i = len(raw)
				} else {
					i += j + 2
					comment = false
				}
			}
			continue
		}

		if esc {
			esc = false
			continue
		}

		if b == '\\' && quoted {
			esc = true
			continue
		}

		if b == '"' || b == '\'' {
			quoted = !quoted
		}

		if b == '/' && !quoted {
			comment = true
			continue
		}

		buf.WriteByte(b)
	}

	if quoted || esc || comment {
		// unexpected state, so return raw bytes
		return raw
	}

	return buf.Bytes()
}
