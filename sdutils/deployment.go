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

package sdutils

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"
)

var (
	pluginMap = make(map[string]Plugin)
)

func AddCloudType(p Plugin) {
	pluginMap[p.GetName()] = p
}

func GetPlugin(name string) (Plugin, error) {
	p, ok := pluginMap[name]
	if !ok {
		return nil, fmt.Errorf("The plugin %s does not exist", name)
	}
	return p, nil
}

func DeploymentDir(confDir string, deploymentName string) string {
	return path.Join(confDir, "deployments", deploymentName)
}

func DeleteDeployment(context AppContext, name string) {
	deploymentDir := DeploymentDir(context.GetConfigDir(), name)
	os.RemoveAll(deploymentDir)
}

func LoadDeployment(context AppContext, baseD *BaseDeployment, new bool) (Deployment, error) {
	confPath := path.Join(baseD.Directory, "config.json")

	plugin, err := GetPlugin(baseD.Type)
	if err != nil {
		return nil, err
	}

	if !new {
		if _, err := os.Stat(confPath); os.IsNotExist(err) {
			return nil, fmt.Errorf("The deployment %s does not exist", baseD.Name)
		}
		data, err := ioutil.ReadFile(confPath)
		if err != nil {
			return nil, err
		}
		err = json.Unmarshal(data, baseD)
		if err != nil {
			return nil, err
		}
		context.Logf(DEBUG, "Loading the default %s from %s", baseD, confPath)
		return plugin.DeploymentLoader(context, baseD, new)
	} else {
		os.MkdirAll(baseD.Directory, 0755)
		d, err := plugin.DeploymentLoader(context, baseD, new)
		if err != nil {
			os.RemoveAll(baseD.Directory)
		}
		return d, err
	}
}

func runClient(context AppContext, baseD *BaseDeployment, d Deployment, cmdArray []string) error {
	sd, err := d.FullStatus()
	if err != nil {
		return err
	}

	baseSSH, err := getSSHCommand(context, baseD, sd)
	if err != nil {
		return nil
	}
	chpwCmd := append(baseSSH,
		"sudo",
		"/usr/local/stardog/bin/stardog-admin",
		"--server",
		sd.StardogInternalURL)
	chpwCmd = append(chpwCmd,
		cmdArray...)

	cmd := exec.Cmd{
		Path: chpwCmd[0],
		Args: chpwCmd,
	}
	_, err = RunCommand(context, cmd, linePrinter, nil)
	return err
}

func getSSHCommand(context AppContext, baseD *BaseDeployment, sd *StardogDescription) ([]string, error) {
	context.Logf(DEBUG, "sshing to %s to run the stardog client\n", sd.SSHHost)

	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return nil, err
	}

	sshCmd := []string{sshPath,
		"-t", "-t",
		"-i", baseD.PrivateKey,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		fmt.Sprintf("%s@%s", "ubuntu", sd.SSHHost),
	}

	return sshCmd, nil
}

func RunSSH(context AppContext, baseD *BaseDeployment, d Deployment) error {
	sd, err := d.FullStatus()
	if err != nil {
		return err
	}

	baseSsh, err := getSSHCommand(context, baseD, sd)
	if err != nil {
		return nil
	}

	cmd := exec.Cmd{
		Path: baseSsh[0],
		Args: baseSsh,
	}
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout

	if err = cmd.Start(); err != nil {
		return err
	}
	if err = cmd.Wait(); err != nil {
		return err
	}
	return nil
}

func IsHealthy(context AppContext, baseD *BaseDeployment, d Deployment, internal bool) bool {
	sd, err := d.FullStatus()
	if err != nil {
		context.Logf(WARN, "Status failure %s", err)
		return false
	}
	if os.Getenv("STARDOG_GRAVITON_UNIT_TEST") != "" {
		h := os.Getenv("STARDOG_GRAVITON_HEALTHY")
		if h != "" {
			b, err := strconv.ParseBool(h)
			if err != nil {
				return false
			}
			return b
		}
		return true
	}

	if internal {
		context.Logf(DEBUG, "Checking health via ssh.")
		sshBase, err := getSSHCommand(context, baseD, sd)
		if err != nil {
			context.Logf(DEBUG, "ssh command error %s", err)
			return false
		}

		sshCmd := append(sshBase, []string{
			"/usr/bin/curl",
			"-s", "-o", "/dev/null",
			"-w", "%{http_code}",
			fmt.Sprintf("%s/admin/healthcheck", sd.StardogInternalURL),
		}...)

		cmd := exec.Cmd{
			Path: sshCmd[0],
			Args: sshCmd,
		}

		context.Logf(INFO, "Running the remote health checker %s.", strings.Join(sshCmd, " "))
		b, err := cmd.Output()
		if err != nil {
			context.Logf(DEBUG, "ssh run error %s", err)
			return false
		}
		return string(b) == "200"

	} else {
		url := fmt.Sprintf("%s/admin/healthcheck", sd.StardogURL)
		context.Logf(DEBUG, "Checking health at %s.", url)

		response, err := http.Get(url)
		if err != nil {
			context.Logf(DEBUG, "Error getting the health check %s", err)
			return false
		}
		return response.StatusCode == 200
	}
}

func WaitForHealth(context AppContext, baseD *BaseDeployment, d Deployment, waitTimeout int, internal bool) error {
	last := ""
	pollInterval := 2
	itCnt := waitTimeout / pollInterval

	var spin *Spinner
	if internal {
		spin = NewSpinner(context, 2, "Waiting for the node to be healthy internally")
	} else {
		spin = NewSpinner(context, 1, "Waiting for external health check to pass")
	}
	for i := 0; !IsHealthy(context, baseD, d, internal); i++ {
		if i >= itCnt {
			return fmt.Errorf("Timed out waiting for the instance to get healthy")
		}
		spin.EchoNext()
		context.ConsoleLog(1, "\r%s", last)
		time.Sleep(time.Duration(pollInterval) * time.Second)
	}
	spin.Close()
	context.ConsoleLog(1, "%s\n", context.SuccessString("The instance is healthy"))
	return nil
}

func linePrinter(cliContext AppContext, line string) *ScanResult {
	cliContext.Logf(DEBUG, line)
	cliContext.ConsoleLog(1, "%s\n", line)
	return nil
}

func CreateInstance(context AppContext, baseD *BaseDeployment, dep Deployment, zkSize int, waitMaxTimeSec int, mask string, noWait bool) error {
	err := dep.CreateInstance(zkSize, mask)
	if err != nil {
		return err
	}
	if noWait {
		context.ConsoleLog(1, "Not waiting...\n")
		return nil
	}

	context.ConsoleLog(1, "Waiting for stardog to come up...\n")
	err = WaitForHealth(context, baseD, dep, waitMaxTimeSec, false)
	if err != nil {
		return err
	}
	return nil
}

func FullStatus(context AppContext, baseD *BaseDeployment, dep Deployment, internal bool, outfile string) error {
	sd, err := dep.FullStatus()
	if err != nil {
		return err
	}
	sd.Healthy = IsHealthy(context, baseD, dep, internal)

	context.ConsoleLog(1, "Stardog is available here: %s\n", context.HighlightString(sd.StardogURL))
	context.ConsoleLog(1, "ssh is available here: %s\n", sd.SSHHost)
	if outfile != "" {
		err = WriteJSON(sd, outfile)
		if err != nil {
			return err
		}
	}
	if sd.Healthy {
		context.ConsoleLog(1, "%s\n", context.SuccessString("The instance is healthy"))
	} else {
		context.ConsoleLog(1, "%s\n", context.FailString("The instance is not healthy"))
	}
	if os.Getenv("STARDOG_GRAVITON_UNIT_TEST") != "" {
		return nil
	}
	return runClient(context, baseD, dep, []string{"cluster", "info"})
}