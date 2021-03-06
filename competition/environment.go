package competition

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"io/ioutil"
	"path/filepath"
	"strings"

	yaml "gopkg.in/yaml.v2"
)

type Vars map[string]string

type Environment struct {
	Name             string   `yaml:"name"`
	Prefix           string   `yaml:"prefix"`
	WhitelistIPs     []string `yaml:"ip_whitelist"`
	Vars             `yaml:"variables"`
	AWSConfig        `yaml:"aws_config"`
	PodCount         int                 `yaml:"pod_count"`
	Domain           string              `yaml:"domain"`
	IncludedNetworks []string            `yaml:"included_networks"`
	ResolvedNetworks map[string]*Network `yaml:"-"`
	Competition      `yaml:"-"`
	Users            []*User    `yaml:"-"`
	Networks         []*Network `yaml:"-"`
	Hosts            []*Host    `yaml:"-"`
	JumpHosts        `yaml:"jump_hosts"`
}

type AWSConfig struct {
	CIDR   string `yaml:"cidr"`
	Region string `yaml:"region"`
	Zone   string `yaml:"zone"`
}

type JumpHosts struct {
	CIDR    string `yaml:"cidr"`
	Windows struct {
		AMI     string   `yaml:"ami"`
		Count   int      `yaml:"count"`
		Size    string   `yaml:"size"`
		Scripts []string `yaml:"scripts"`
	} `yaml:"windows"`
	Kali struct {
		AMI     string   `yaml:"ami"`
		Count   int      `yaml:"count"`
		Size    string   `yaml:"size"`
		Scripts []string `yaml:"scripts"`
	} `yaml:"kali"`
}

func (e *Environment) EnvRoot() string {
	return filepath.Join(GetHome(), "environments", e.Name)
}

func (e *Environment) NetworksDir() string {
	return filepath.Join(e.EnvRoot(), "networks")
}

func (e *Environment) HostsDir() string {
	return filepath.Join(e.EnvRoot(), "hosts")
}

func (e *Environment) TfDir() string {
	return filepath.Join(e.EnvRoot(), "terraform")
}

func (e *Environment) TfFile() string {
	return filepath.Join(e.TfDir(), "infra.tf")
}

func (e *Environment) TfScriptsDir() string {
	return filepath.Join(e.EnvRoot(), "terraform", "scripts")
}

func (e *Environment) DefaultCIDR() string {
	return "10.0.0.0/8"
}

func (e *Environment) PodPassword(podID int) string {
	return DeterminedPassword(fmt.Sprintf("%s-%d", e.Name, podID))
}

func LoadEnvironment(name string) (*Environment, error) {
	envConfigFile := filepath.Join(GetHome(), "environments", name, "env.yml")
	envNetworkPath := filepath.Join(GetHome(), "environments", name, "networks")
	envHostPath := filepath.Join(GetHome(), "environments", name, "hosts")
	if !PathExists(envConfigFile) {
		return nil, errors.New("not a valid environment: no env.yml file")
	}
	if !PathExists(envNetworkPath) {
		return nil, errors.New("not a valid environment: no networks directory located at " + envNetworkPath)
	}
	if !PathExists(envHostPath) {
		return nil, errors.New("not a valid environment: no networks directory located at " + envNetworkPath)
	}
	env := Environment{}
	envConfig, err := ioutil.ReadFile(envConfigFile)
	if err != nil {
		return nil, err
	}
	err = yaml.Unmarshal(envConfig, &env)
	if err != nil {
		return nil, err
	}
	env.ResolvedNetworks = env.ResolveIncludedNetworks()
	for _, network := range env.ResolvedNetworks {
		network.ResolvedHosts = network.ResolveIncludedHosts()
	}
	return &env, nil
}

func (e *Environment) ParseHosts() map[string]*Host {
	hosts := make(map[string]*Host)
	hostFiles, _ := filepath.Glob(filepath.Join(e.HostsDir(), "*.yml"))
	for _, file := range hostFiles {
		if filepath.Base(file) == ".gitkeep" {
			continue
		}
		host, err := LoadHostFromFile(file)
		if err != nil {
			LogError("Error reading host file: " + file)
			continue
		}
		hosts[FileToName(file)] = host
	}

	return hosts
}

func (e *Environment) ParseNetworks() map[string]*Network {
	networks := make(map[string]*Network)
	networkFiles, _ := filepath.Glob(filepath.Join(e.NetworksDir(), "*.yml"))
	for _, file := range networkFiles {
		if filepath.Base(file) == ".gitkeep" {
			continue
		}
		network, err := LoadNetworkFromFile(file)
		if err != nil {
			LogError("Error reading network file: " + file)
			continue
		}
		networks[FileToName(file)] = network
	}

	return networks
}

func (e *Environment) ResolveIncludedNetworks() map[string]*Network {
	networks := make(map[string]*Network)
	networkFiles, _ := filepath.Glob(filepath.Join(e.NetworksDir(), "*.yml"))
	for _, file := range networkFiles {
		if filepath.Base(file) == ".gitkeep" {
			continue
		}
		if !Contains(FileToName(file), e.IncludedNetworks) {
			continue
		}
		network, err := LoadNetworkFromFile(file)
		if err != nil {
			LogError("Error reading network file: " + file)
			continue
		}
		network.Environment = *e
		networks[FileToName(file)] = network
	}

	return networks
}

func (e *Environment) ResolvePublicTCP() map[int]bool {
	portMap := map[int]bool{}
	networks := e.ResolveIncludedNetworks()
	for _, network := range networks {
		hosts := network.ResolveIncludedHosts()
		for _, host := range hosts {
			for _, port := range host.TCPPorts {
				portMap[port] = true
			}
		}
	}
	return portMap
}

func (e *Environment) ResolvePublicUDP() map[int]bool {
	portMap := map[int]bool{}
	networks := e.ResolveIncludedNetworks()
	for _, network := range networks {
		hosts := network.ResolveIncludedHosts()
		for _, host := range hosts {
			for _, port := range host.UDPPorts {
				portMap[port] = true
			}
		}
	}
	return portMap
}

func (e *Environment) Suffix(podOffset int) string {
	return fmt.Sprintf("%s%d", e.Prefix, podOffset)
}

func (e *Environment) TFName(name string, offset int) string {
	return fmt.Sprintf("%s_%s", name, e.Suffix(offset))
}

func (e *Environment) CreateNetwork(n *Network) {
	networks := e.ParseNetworks()
	if networks[n.Name] != nil {
		LogFatal("You cannot create a network that already exists!")
	}

	var tpl bytes.Buffer
	tmpl, err := template.New(n.Name).Parse(string(MustAsset("network.yml")))
	if err != nil {
		LogFatal("Fatal error parsing network config template: " + err.Error())
	}
	if err := tmpl.Execute(&tpl, n); err != nil {
		LogFatal("Fatal error rendering network config: " + err.Error())
	}
	filename := filepath.Join(e.NetworksDir(), strings.ToLower(n.Name)+".yml")
	err = ioutil.WriteFile(filename, tpl.Bytes(), 0644)
	if err != nil {
		LogFatal("Error writing network config file: " + err.Error())
	}
	Log("Network created: " + n.Name)
}

func (e *Environment) CreateHost(h *Host) {
	hosts := e.ParseHosts()
	if hosts[strings.ToLower(h.Hostname)] != nil {
		LogFatal("You cannot create a host that already exists!")
	}

	var tpl bytes.Buffer
	tmpl, err := template.New(h.Hostname).Parse(string(MustAsset("host.yml")))
	if err != nil {
		LogFatal("Fatal error parsing host config template: " + err.Error())
	}
	if err := tmpl.Execute(&tpl, h); err != nil {
		LogFatal("Fatal error rendering host config: " + err.Error())
	}
	filename := filepath.Join(e.HostsDir(), strings.ToLower(h.Hostname)+".yml")
	err = ioutil.WriteFile(filename, tpl.Bytes(), 0644)
	if err != nil {
		LogFatal("Error writing host config file: " + err.Error())
	}
	Log("Host created: " + h.Hostname)
}

func (e *Environment) KaliJumpAMI() string {
	if e.JumpHosts.Kali.AMI != "" {
		return e.JumpHosts.Kali.AMI
	}
	return AMIMap["ubuntu"].Regions[e.AWSConfig.Region]
}

func (e *Environment) WindowsJumpAMI() string {
	if e.JumpHosts.Windows.AMI != "" {
		return e.JumpHosts.Windows.AMI
	}
	return AMIMap["w2k16"].Regions[e.AWSConfig.Region]
}
