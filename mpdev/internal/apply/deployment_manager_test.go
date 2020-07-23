// Copyright 2020 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package apply

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"gopkg.in/yaml.v3"
	"k8s.io/utils/exec"
	testingexec "k8s.io/utils/exec/testing"
)

func TestDeploymentManager(t *testing.T) {
	wd, err := os.Getwd()
	assert.NoError(t, err)

	testcases := []struct {
		name            string
		expectedRunArgs [][]string
		zipFilePath     string
		missingRef      bool
		badRefType      bool
	}{{
		name: "Deployment Manager GCS",
		expectedRunArgs: [][]string{
			{"zip", "-r", "/tmp/outdir/dm_template.zip", "."},
			{"gsutil", "cp", "/tmp/outdir/dm_template.zip", "gs://project/dmtemppath.zip"},
		},
		zipFilePath: "gs://project/dmtemppath.zip",
	}, {
		name: "Deployment Manager Local Save Relative Path",
		expectedRunArgs: [][]string{
			{"zip", "-r", filepath.Join(wd, "resourcedir/dir2/localzippath.zip"), "."},
		},
		zipFilePath: "dir2/localzippath.zip",
	},
		{
			name: "Deployment Manager Local Save Absolute Path",
			expectedRunArgs: [][]string{
				{"zip", "-r", "/tmp/dir3/localzippath.zip", "."},
			},
			zipFilePath: "/tmp/dir3/localzippath.zip",
		},
		{
			name:        "Deployment Manager Missing Reference",
			zipFilePath: "/tmp/dir4/localzippath.zip",
			missingRef:  true,
		},
		{
			name:        "Deployment Manager Bad Reference Type",
			zipFilePath: "/tmp/dir5/localzippath.zip",
			badRefType:  true,
		},
	}
	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			fcmd := testingexec.FakeCmd{
				RunScript: []testingexec.FakeRunAction{
					func() ([]byte, []byte, error) { return nil, nil, nil },
					func() ([]byte, []byte, error) { return nil, nil, nil },
				},
			}

			executor := &testingexec.FakeExec{
				CommandScript: []testingexec.FakeCommandAction{
					func(cmd string, args ...string) exec.Cmd { return testingexec.InitFakeCmd(&fcmd, cmd, args...) },
					func(cmd string, args ...string) exec.Cmd { return testingexec.InitFakeCmd(&fcmd, cmd, args...) },
				},
			}
			r := NewRegistry(executor)

			autogen := getDeploymentManagerAutogenTemplate()
			autogen.outDir = "/tmp/outdir"

			dm := &DeploymentManagerTemplate{
				BaseResource: BaseResource{
					TypeMeta{
						APIVersion: apiVersion,
						Kind:       "DeploymentManagerTemplate",
					},
					Metadata{Name: "dm-temp"},
				},
				DeploymentManagerRef: autogen.GetReference(),
				ZipFilePath:          tc.zipFilePath,
			}

			if tc.missingRef {
				dm.DeploymentManagerRef = Reference{}
			}

			if tc.badRefType {
				dm.DeploymentManagerRef = dm.GetReference()
			}

			dir := "resourcedir"
			r.RegisterResource(autogen, dir)
			r.RegisterResource(dm, dir)

			err := dm.Apply(r)

			if tc.missingRef || tc.badRefType {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			assert.Equal(t, len(tc.expectedRunArgs), fcmd.RunCalls)
			assert.Equal(t, tc.expectedRunArgs, fcmd.RunLog)
		})
	}
}

var autogenSpecStr = `
partnerId: testPartner
solutionId: testSolution
spec:
  singleVm:
    adminUrl:
      path: wp-admin
      scheme: HTTP
    applicationStatus:
      type: WAITER
      waiter:
        waiterTimeoutSecs: 300
    bootDisk:
      diskSize:
        defaultSizeGb: 10
        minSizeGb: 10
      diskType:
        defaultType: pd-standard
    gceMetadataItems:
    - key: installphpmyadmin
      valueFromDeployInputField: installphpmyadmin
    deployInput:
      sections:
      - fields:
        - name: installphpmyadmin
          title: Install phpMyAdmin
          description: phpMyAdmin is an open source tool to administer MySQL databases
          booleanCheckbox:
          default_value: true
          placement: MAIN
`
var autogenSpec interface{}

func TestAutogen(t *testing.T) {
	err := yaml.Unmarshal([]byte(autogenSpecStr), &autogenSpec)
	assert.NoError(t, err)

	autogen := getDeploymentManagerAutogenTemplate()

	mountRegex := regexp.MustCompile("type=bind,src=/tmp/autogen(.*),dst=/autogen")
	// docker run argv index for mounting autogen input file
	mountIdx := 7
	fcmd := testingexec.FakeCmd{}

	fcmd.RunScript = []testingexec.FakeRunAction{
		func() ([]byte, []byte, error) {
			mountMatch := mountRegex.FindStringSubmatch(fcmd.Argv[mountIdx])
			assert.Equal(t, 2, len(mountMatch))

			f, err := os.Open(fmt.Sprintf("/tmp/autogen%s/autogen.yaml", mountMatch[1]))
			assert.NoError(t, err)

			// Check that input file to autogen container matches autogenSpec
			dec := yaml.NewDecoder(f)
			var specOnFile interface{}
			err = dec.Decode(&specOnFile)
			assert.NoError(t, err)
			assert.Equal(t, autogenSpec, specOnFile)
			return nil, nil, nil
		},
	}

	executor := &testingexec.FakeExec{
		CommandScript: []testingexec.FakeCommandAction{
			func(cmd string, args ...string) exec.Cmd { return testingexec.InitFakeCmd(&fcmd, cmd, args...) },
		},
	}

	r := NewRegistry(executor)
	dir := "dir2"
	r.RegisterResource(autogen, dir)
	err = r.Apply()
	assert.NoError(t, err)

	assert.Equal(t, 1, fcmd.RunCalls)
	// Temp directory created is random so check that bind mount matches regex
	assert.Regexp(t, mountRegex, fcmd.RunLog[0][mountIdx])

	expectedArgs := []string{
		"docker", "run", "--rm", "-i", "--mount", fmt.Sprintf("type=bind,src=%s,dst=/tmp/out", autogen.outDir),
		"--mount", fcmd.RunLog[0][mountIdx], "gcr.io/cloud-marketplace-tools/dm/autogen",
		"--input_type", "YAML", "--single_input", "/autogen/autogen.yaml",
		"--output_type", "PACKAGE", "--output", "/tmp/out",
	}

	assert.Equal(t, expectedArgs, fcmd.RunLog[0])
}

func getDeploymentManagerAutogenTemplate() *DeploymentManagerAutogenTemplate {
	autogen := &DeploymentManagerAutogenTemplate{
		BaseResource: BaseResource{
			TypeMeta{
				APIVersion: apiVersion,
				Kind:       "DeploymentManagerAutogenTemplate",
			},
			Metadata{Name: "autogen"},
		},
		PartnerID:   "testPartner1",
		SolutionID:  "testSolution1",
		AutogenSpec: autogenSpec,
	}
	return autogen
}