//
//  Copyright (c) 2017, Stardog Union. <http://stardog.com>
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package aws

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"

	"github.com/stardog-union/stardog-graviton/sdutils"
)

type AwsEc2Instance struct {
	DeploymentName         string             `json:"deployment_name,omitempty"`
	Region                 string             `json:"aws_region,omitempty"`
	KeyName                string             `json:"aws_key_name,omitempty"`
	Version                string             `json:"version,omitempty"`
	ZkInstanceType         string             `json:"zk_instance_type,omitempty"`
	SdInstanceType         string             `json:"stardog_instance_type,omitempty"`
	ZkSize                 string             `json:"zookeeper_size,omitempty"`
	SdSize                 string             `json:"stardog_size,omitempty"`
	AmiID                  string             `json:"baseami,omitempty"`
	PrivateKey             string             `json:"private_key,omitempty"`
	HTTPMask               string             `json:"http_subnet,omitempty"`
	DeployDir              string             `json:"-"`
	Ctx                    sdutils.AppContext `json:"-"`
	BastionContact         string             `json:"-"`
	StardogContact         string             `json:"-"`
	StardogInternalContact string             `json:"-"`
	ZkNodesContact         []string           `json:"-"`
}

type AwsInstanceStatusDescription struct {
	ZkNodesContact []string
}

func NewEc2Instance(ctx sdutils.AppContext, dd *awsDeploymentDescription) *AwsEc2Instance {
	instance := AwsEc2Instance{
		DeploymentName: dd.Name,
		Region:         dd.Region,
		KeyName:        dd.AwsKeyName,
		Version:        dd.Version,
		ZkInstanceType: dd.ZkInstanceType,
		SdInstanceType: dd.SdInstanceType,
		AmiID:          dd.AmiID,
		PrivateKey:     dd.PrivateKeyPath,
		DeployDir:      dd.deployDir,
		Ctx:            ctx,
	}
	return &instance
}

func volumeLineScanner(cliContext sdutils.AppContext, line string) *sdutils.ScanResult {
	outputKeys := []string{"load_balancer_ip"}

	for _, k := range outputKeys {
		if strings.HasPrefix(line, k) {
			la := strings.Split(line, " = ")
			return &sdutils.ScanResult{Key: la[0], Value: la[1]}
		}
	}
	return nil
}

func (awsI *AwsEc2Instance) CreateInstance(zookeeperSize int, mask string) error {
	vol, err := LoadEbsVolume(awsI.Ctx, path.Join(awsI.DeployDir, "etc", "terraform", "volumes"))
	if err != nil {
		return err
	}

	awsI.ZkSize = fmt.Sprintf("%d", zookeeperSize)
	awsI.SdSize = vol.ClusterSize
	awsI.HTTPMask = mask

	instanceWorkingDir := path.Join(awsI.DeployDir, "etc", "terraform", "instance")
	instanceConfPath := path.Join(instanceWorkingDir, "instance.json")
	if sdutils.PathExists(instanceConfPath) {
		awsI.Ctx.ConsoleLog(1, "The instance already exists.\n")
		awsI.Ctx.Logf(sdutils.WARN, "The instance already exists.")
	}
	err = sdutils.WriteJSON(awsI, instanceConfPath)
	if err != nil {
		return err
	}

	terraformPath, err := exec.LookPath("terraform")
	if err != nil {
		return err
	}

	cmdArray := []string{terraformPath, "apply", "-var-file",
		instanceConfPath}
	cmd := exec.Cmd{
		Path: cmdArray[0],
		Args: cmdArray,
		Dir:  instanceWorkingDir,
	}
	awsI.Ctx.Logf(sdutils.INFO, "Running terraform...\n")
	spin := sdutils.NewSpinner(awsI.Ctx, 1, "Creating the instance VMs")
	_, err = sdutils.RunCommand(awsI.Ctx, cmd, volumeLineScanner, spin)
	if err != nil {
		return err
	}
	awsI.Ctx.ConsoleLog(1, "Successfully created the instance.\n")

	return nil
}

func (awsI *AwsEc2Instance) DeleteInstance() error {
	instanceWorkingDir := path.Join(awsI.DeployDir, "etc", "terraform", "instance")
	instanceConfPath := path.Join(instanceWorkingDir, "instance.json")
	if !sdutils.PathExists(instanceConfPath) {
		return fmt.Errorf("There is no configured instance")
	}
	terraformPath, err := exec.LookPath("terraform")
	if err != nil {
		return err
	}
	cmdArray := []string{terraformPath, "destroy", "-force", "-var-file", instanceConfPath}
	cmd := exec.Cmd{
		Path: cmdArray[0],
		Args: cmdArray,
		Dir:  instanceWorkingDir,
	}
	awsI.Ctx.Logf(sdutils.INFO, "Running terraform...\n")
	spin := sdutils.NewSpinner(awsI.Ctx, 1, "Deleting the instance VMs")
	_, err = sdutils.RunCommand(awsI.Ctx, cmd, volumeLineScanner, spin)
	if err != nil {
		return err
	}
	os.Remove(instanceConfPath)
	awsI.Ctx.ConsoleLog(1, "Successfully destroyed the instance.\n")
	return nil
}

func (awsI *AwsEc2Instance) InstanceExists() bool {
	instanceWorkingDir := path.Join(awsI.DeployDir, "etc", "terraform", "instance")
	instanceConfPath := path.Join(instanceWorkingDir, "instance.json")
	return sdutils.PathExists(instanceConfPath)
}

type OutputEntry struct {
	Sensitive bool        `json:"sensitive,omitempty"`
	Type      string      `json:"type,omitempty"`
	Value     interface{} `json:"value,omitempty"`
}

func getInstanceValues(awsI *AwsEc2Instance) (*AwsInstanceStatusDescription, error) {
	instanceWorkingDir := path.Join(awsI.DeployDir, "etc", "terraform", "instance")
	instanceConfPath := path.Join(instanceWorkingDir, "instance.json")
	if !sdutils.PathExists(instanceConfPath) {
		return nil, fmt.Errorf("There is no configured instance")
	}
	terraformPath, err := exec.LookPath("terraform")
	if err != nil {
		return nil, err
	}
	cmdArray := []string{terraformPath, "output", "-json"}
	cmd := exec.Cmd{
		Path: cmdArray[0],
		Args: cmdArray,
		Dir:  instanceWorkingDir,
	}
	data, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	try := make(map[string]OutputEntry)
	err = json.Unmarshal(data, &try)
	if err != nil {
		return nil, err
	}

	awsI.StardogInternalContact = try["stardog_internal_contact"].Value.(string)
	awsI.StardogContact = try["stardog_contact"].Value.(string)
	awsI.BastionContact = try["bastion_contact"].Value.(string)
	interList := try["zookeeper_nodes"].Value.([]interface{})
	awsI.ZkNodesContact = make([]string, len(interList), len(interList))
	for ndx, x := range interList {
		awsI.ZkNodesContact[ndx] = x.(string)
	}

	s := AwsInstanceStatusDescription{
		ZkNodesContact: awsI.ZkNodesContact,
	}

	return &s, nil
}

func (awsI *AwsEc2Instance) Status() error {
	_, err := getInstanceValues(awsI)
	if err != nil {
		return err
	}

	awsI.Ctx.ConsoleLog(1, "Stardog: %s\n", fmt.Sprintf("http://%s:5821", awsI.StardogContact))
	awsI.Ctx.ConsoleLog(1, "SSH: %s\n", awsI.BastionContact)
	return nil
}