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

	"github.com/stardog-union/stardog-graviton/sdutils"
)

type AwsVolumeStatusDescription struct {
	VolumeIds []string
}

type AwsEbsVolumes struct {
	DeploymentName   string             `json:"deployment_name,omitempty"`
	Region           string             `json:"aws_region,omitempty"`
	SizeOfEachVolume string             `json:"storage_size,omitempty"`
	ClusterSize      string             `json:"cluster_size,omitempty"`
	AwsKeyName       string             `json:"aws_key_name,omitempty"`
	KeyPath          string             `json:"key_path,omitempty"`
	AmiID            string             `json:"ami,omitempty"`
	InstanceType     string             `json:"instance_type,omitempty"`
	LicensePath      string             `json:"stardog_license,omitempty"`
	VolumeDir        string             `json:"-"`
	appContext       sdutils.AppContext `json:"-"`
}

func NewAwsEbsVolumeManager(ac sdutils.AppContext, dd *awsDeploymentDescription) *AwsEbsVolumes {
	volumeDir := path.Join(dd.deployDir, "etc", "terraform", "volumes")
	return &AwsEbsVolumes{
		DeploymentName: dd.Name,
		Region:         dd.Region,
		AwsKeyName:     dd.AwsKeyName,
		KeyPath:        dd.PrivateKeyPath,
		AmiID:          dd.AmiID,
		InstanceType:   dd.SdInstanceType,
		VolumeDir:      volumeDir,
		appContext:     ac,
	}
}

func LoadEbsVolume(ac sdutils.AppContext, volDir string) (*AwsEbsVolumes, error) {
	var ebsC AwsEbsVolumes
	confFile := path.Join(volDir, "config.json")
	err := sdutils.LoadJSON(&ebsC, confFile)
	if err != nil {
		return nil, err
	}
	return &ebsC, nil
}

func (v *AwsEbsVolumes) VolumeExists() bool {
	confFile := path.Join(v.VolumeDir, "config.json")
	return sdutils.PathExists(confFile)
}

func (v *AwsEbsVolumes) CreateSet(licensePath string, sizeOfEachVolume int, clusterSize int) error {
	// TODO make sure we clean up resources on failure
	v.appContext.ConsoleLog(2, "Creating an aws volume set in directory %s\n", v.VolumeDir)
	terraformPath, err := exec.LookPath("terraform")
	if err != nil {
		return err
	}
	v.ClusterSize = fmt.Sprintf("%d", clusterSize)
	v.SizeOfEachVolume = fmt.Sprintf("%d", sizeOfEachVolume)
	v.LicensePath = licensePath

	confFile := path.Join(v.VolumeDir, "config.json")
	if _, err := os.Stat(confFile); err == nil {
		v.appContext.ConsoleLog(1, "Volumes have already been created for the %s deployment, running terraform apply again.", v.DeploymentName)
		v.appContext.Logf(sdutils.WARN, "Volumes have already been created for the %s deployment, running terraform apply again.", v.DeploymentName)
	}
	err = sdutils.WriteJSON(v, confFile)
	if err != nil {
		return err
	}

	cmdArray := []string{terraformPath, "apply",
		"-var-file", confFile}
	cmd := exec.Cmd{
		Path: cmdArray[0],
		Args: cmdArray,
		Dir:  v.VolumeDir,
	}
	spin := sdutils.NewSpinner(v.appContext, 1, "Calling out to terraform to create the volumes")
	_, err = sdutils.RunCommand(v.appContext, cmd, nil, spin)
	if err != nil {
		return err
	}
	err = os.Remove(path.Join(v.VolumeDir, "builder.tf"))
	if err != nil {
		return err
	}
	spin = sdutils.NewSpinner(v.appContext, 1, "Calling out to terraform to stop builder instances")
	_, err = sdutils.RunCommand(v.appContext, cmd, nil, spin)
	if err != nil {
		return err
	}
	v.appContext.ConsoleLog(1, "Successfully created the volumes.\n")
	return nil
}

func (v *AwsEbsVolumes) DeleteSet() error {
	confFile := path.Join(v.VolumeDir, "config.json")
	terraformPath, err := exec.LookPath("terraform")
	if err != nil {
		return err
	}
	cmdArray := []string{terraformPath, "destroy", "-force",
		"-var-file", confFile}

	cmd := exec.Cmd{
		Path: cmdArray[0],
		Args: cmdArray,
		Dir:  v.VolumeDir,
	}
	spin := sdutils.NewSpinner(v.appContext, 1, "Calling out to terraform to delete the images")
	_, err = sdutils.RunCommand(v.appContext, cmd, nil, spin)
	if err != nil {
		return err
	}
	err = os.Remove(confFile)
	if err != nil {
		return err
	}
	v.appContext.ConsoleLog(1, "Successfully destroyed the volumes.\n")
	return nil
}

func (v *AwsEbsVolumes) getStatusInformation() (*AwsVolumeStatusDescription, error) {
	terraformPath, err := exec.LookPath("terraform")
	if err != nil {
		return nil, err
	}

	cmdArray := []string{terraformPath, "output", "-json"}
	cmd := exec.Cmd{
		Path: cmdArray[0],
		Args: cmdArray,
		Dir:  v.VolumeDir,
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
	volStatus := AwsVolumeStatusDescription{}
	volsEnt, ok := try["volumes"]
	if !ok {
		return nil, fmt.Errorf("Invalid volume results in terraform output")
	}
	interList := volsEnt.Value.([]interface{})

	volStatus.VolumeIds = make([]string, len(interList), len(interList))
	for ndx, x := range interList {
		volStatus.VolumeIds[ndx] = x.(string)
	}

	return &volStatus, nil
}

func (v *AwsEbsVolumes) Status() error {
	vD, err := v.getStatusInformation()
	if err != nil {
		return err
	}
	v.appContext.ConsoleLog(1, "Volumes:\n")
	for _, x := range vD.VolumeIds {
		v.appContext.ConsoleLog(1, "%s\n", x)
	}
	return nil
}