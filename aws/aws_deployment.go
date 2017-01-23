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
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"strings"

	"github.com/stardog-union/stardog-graviton/sdutils"
)

type awsDeploymentDescription struct {
	Region         string             `json:"region,omitempty"`
	AmiID          string             `json:"ami_id,omitempty"`
	AwsKeyName     string             `json:"keyname,omitempty"`
	ZkInstanceType string             `json:"zk_instance,omitempty"`
	SdInstanceType string             `json:"sd_instance,omitempty"`
	PrivateKeyPath string             `json:"private_key_path,omitempty"`
	CustomProps    string             `json:"custom_stardog_properties,omitempty"`
	HTTPMask       string             `json:"http_mask,omitempty"`
	Version        string             `json:"-"`
	Name           string             `json:"-"`
	deployDir      string             `json:"-"`
	ctx            sdutils.AppContext `json:"-"`
}

func newAwsDeploymentDescription(c sdutils.AppContext, baseD *sdutils.BaseDeployment, a *awsPlugin) (*awsDeploymentDescription, error) {
	var err error

	if a.AmiID == "" {
		// If the ami is not specified look it up
		amiMap, err := loadAmiAmp(c)
		if err != nil {
			return nil, fmt.Errorf("Could not load the ami map", err)
		}
		ami, ok := amiMap[a.Region]
		if !ok {
			// No ami for the deployment
			c.ConsoleLog(1, "A base AMI is required for launching the virtual appliance.  If you do not know this value you can build a new one with the 'baseami' command.\n")
			ami, err = sdutils.AskUser("Stardog base AMI", "")
			if err != nil {
				return nil, err
			}
			if ami == "" {
				return nil, fmt.Errorf("An AMI is required.  Please see the 'baseami' subcommand")
			}
		}
		a.AmiID = ami
	}
	if a.AwsKeyName == "" {
		a.AwsKeyName, err = sdutils.AskUser("EC2 keyname", "default")
		if err != nil {
			return nil, err
		}
	}
	if a.Region == "" {
		a.AwsKeyName, err = sdutils.AskUser("Region", "us-west-1")
		if err != nil {
			return nil, err
		}
	}
	if baseD.PrivateKey == "" {
		baseD.PrivateKey, err = sdutils.AskUser("Private key path", "")
		if err != nil {
			return nil, err
		}
		if baseD.PrivateKey == "" {
			return nil, fmt.Errorf("A path to a private key must be provided")
		}
	}

	deployDir := sdutils.DeploymentDir(c.GetConfigDir(), baseD.Name)
	assertDir, err := PlaceAsset(c, deployDir, "etc/terraform", false)
	if err != nil {
		return nil, err
	}
	c.ConsoleLog(2, "Terraform configuration extracted to %s\n", assertDir)
	customData := ""
	if baseD.CustomPropsFile != "" {
		data, err := ioutil.ReadFile(baseD.CustomPropsFile)
		if err != nil {
			return nil, err
		}
		customData = string(data)
	}

	dd := awsDeploymentDescription{
		Region:         a.Region,
		AmiID:          a.AmiID,
		AwsKeyName:     a.AwsKeyName,
		ZkInstanceType: a.ZkInstanceType,
		SdInstanceType: a.SdInstanceType,
		Version:        baseD.Version,
		Name:           baseD.Name,
		PrivateKeyPath: baseD.PrivateKey,
		ctx:            c,
		deployDir:      deployDir,
		CustomProps:    customData,
	}
	return &dd, nil
}

func (dd *awsDeploymentDescription) CreateVolumeSet(licensePath string, sizeOfEachVolume int, clusterSize int) error {
	vm := NewAwsEbsVolumeManager(dd.ctx, dd)
	return vm.CreateSet(licensePath, sizeOfEachVolume, clusterSize)
}

func (dd *awsDeploymentDescription) DeleteVolumeSet() error {
	vm := NewAwsEbsVolumeManager(dd.ctx, dd)
	return vm.DeleteSet()
}

func (dd *awsDeploymentDescription) StatusVolumeSet() error {
	vm := NewAwsEbsVolumeManager(dd.ctx, dd)
	return vm.Status()
}

func (dd *awsDeploymentDescription) VolumeExists() bool {
	vm := NewAwsEbsVolumeManager(dd.ctx, dd)
	return vm.VolumeExists()
}

func (dd *awsDeploymentDescription) CreateInstance(zookeeperSize int, mask string) error {
	im := NewEc2Instance(dd.ctx, dd)
	return im.CreateInstance(zookeeperSize, mask)
}

func (dd *awsDeploymentDescription) DeleteInstance() error {
	im := NewEc2Instance(dd.ctx, dd)
	return im.DeleteInstance()
}

func (dd *awsDeploymentDescription) StatusInstance() error {
	im := NewEc2Instance(dd.ctx, dd)
	return im.Status()
}

func (dd *awsDeploymentDescription) FullStatus() (*sdutils.StardogDescription, error) {
	vm := NewAwsEbsVolumeManager(dd.ctx, dd)
	volumeStatus, err := vm.getStatusInformation()
	if err != nil {
		dd.ctx.ConsoleLog(1, "No volume information found.\n")
	}

	im := NewEc2Instance(dd.ctx, dd)
	instS, err := getInstanceValues(im)
	if err != nil {
		dd.ctx.ConsoleLog(1, "No instance information found.\n")
	}

	sD := sdutils.StardogDescription{
		SSHHost:             im.BastionContact,
		StardogURL:          fmt.Sprintf("http://%s:5821", im.StardogContact),
		StardogInternalURL:  fmt.Sprintf("http://%s:5821", im.StardogInternalContact),
		VolumeDescription:   volumeStatus,
		InstanceDescription: instS,
	}

	return &sD, nil
}

func (dd *awsDeploymentDescription) InstanceExists() bool {
	im := NewEc2Instance(dd.ctx, dd)
	return im.InstanceExists()
}

type awsPlugin struct {
	Region         string `json:"region,omitempty"`
	AmiID          string `json:"ami_id,omitempty"`
	AwsKeyName     string `json:"aws_key_name,omitempty"`
	ZkInstanceType string `json:"zk_instance_type,omitempty"`
	SdInstanceType string `json:"sd_instance_type,omitempty"`
	BastionType    string `json:"bastion_instance_type,omitempty"`
}

func GetPlugin() sdutils.Plugin {
	return &awsPlugin{
		Region:         "us-west-1",
		AmiID:          "",
		AwsKeyName:     "",
		ZkInstanceType: "t2.small",
		SdInstanceType: "m3.medium",
		BastionType:    "t2.small",
	}
}

func (a *awsPlugin) LoadDefaults(defaultCliOpts interface{}) error {
	// parse out from the interface any config file defaults
	b, err := json.Marshal(defaultCliOpts)
	if err != nil {
		return err
	}
	err = json.Unmarshal(b, a)
	if err != nil {
		return err
	}
	return nil
}

func (a *awsPlugin) Register(cmdOpts *sdutils.CommandOpts) error {
	cmdOpts.BuildCmd.Flag("region", fmt.Sprintf("The aws region to use [%s].", strings.Join(ValidRegions, " | "))).Default(a.Region).StringVar(&a.Region)

	cmdOpts.LaunchCmd.Flag("region", fmt.Sprintf("The aws region to use [%s]", strings.Join(ValidRegions, " | "))).Default(a.Region).StringVar(&a.Region)
	cmdOpts.LaunchCmd.Flag("zk-instance-type", "The instance type to use for zookeeper VMs").Default(a.ZkInstanceType).StringVar(&a.ZkInstanceType)
	cmdOpts.LaunchCmd.Flag("sd-instance-type", "The instance type to use for stardog VMs").Default(a.SdInstanceType).StringVar(&a.SdInstanceType)
	cmdOpts.LaunchCmd.Flag("aws-key-name", "The AWS ssh key name.").Default(a.AwsKeyName).StringVar(&a.AwsKeyName)

	cmdOpts.LeaksCmd.Flag("region", fmt.Sprintf("The aws region to use [%s]", strings.Join(ValidRegions, " | "))).Default(a.Region).StringVar(&a.Region)

	cmdOpts.NewDeploymentCmd.Flag("region", fmt.Sprintf("The aws region to use [%s].", strings.Join(ValidRegions, " | "))).Default(a.Region).StringVar(&a.Region)
	cmdOpts.NewDeploymentCmd.Flag("zk-instance-type", "The instance type to use for zookeeper VMs.").Default("m3.large").StringVar(&a.ZkInstanceType)
	cmdOpts.NewDeploymentCmd.Flag("sd-instance-type", "The instance type to use for stardog VMs.").Default("m3.large").StringVar(&a.SdInstanceType)
	cmdOpts.NewDeploymentCmd.Flag("aws-key-name", "The AWS ssh key name.").Default(a.AwsKeyName).StringVar(&a.AwsKeyName)

	return nil
}

func (a *awsPlugin) DeploymentLoader(context sdutils.AppContext, baseD *sdutils.BaseDeployment, new bool) (sdutils.Deployment, error) {
	neededEnvs := []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"}
	for _, e := range neededEnvs {
		if os.Getenv(e) == "" {
			return nil, fmt.Errorf("The environment variable %s must be set", e)
		}
	}
	neededPgms := []string{"terraform", "packer"}
	for _, e := range neededPgms {
		_, err := exec.LookPath(e)
		if err != nil {
			return nil, fmt.Errorf("The program %s must be in the path when running this program", e)
		}
	}

	if new {
		awsDD, err := newAwsDeploymentDescription(context, baseD, a)
		if err != nil {
			return nil, err
		}
		baseD.CloudOpts = awsDD
		data, err := json.Marshal(baseD)
		if err != nil {
			return nil, err
		}
		confPath := path.Join(awsDD.deployDir, "config.json")
		err = ioutil.WriteFile(confPath, data, 0600)
		if err != nil {
			return nil, err
		}

		return awsDD, nil
	} else {
		data, err := json.Marshal(baseD.CloudOpts)
		if err != nil {
			return nil, err
		}
		var dd awsDeploymentDescription
		err = json.Unmarshal(data, &dd)
		if err != nil {
			return nil, err
		}
		dd.Name = baseD.Name
		dd.Version = baseD.Version
		dd.ctx = context
		dd.deployDir = sdutils.DeploymentDir(context.GetConfigDir(), baseD.Name)

		return &dd, nil
	}
}

func (a *awsPlugin) GetName() string {
	return "aws"
}