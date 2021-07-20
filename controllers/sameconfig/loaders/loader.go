package loaders

import (
	"fmt"
	"io/ioutil"

	netUrl "net/url"

	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
)

// // Empty struct - used to implement Converter interface.
// type V1 struct {
// }

// LoadSameConfig takes the loaded definiton file, loads it and then unmarshalls it into a SameConfig struct.
func (v V1) unMarshallSAME(def interface{}) (sameConfig *SameConfig, err error) {
	// First create the struct to unmarshall the yaml into
	sameConfigFromFile := &SameSpec{}

	bytes, err := yaml.Marshal(def)
	if err != nil {
		return nil, fmt.Errorf("could not marshal input file into bytes: %v", err)
	}
	err = yaml.Unmarshal(bytes, sameConfigFromFile)
	if err != nil {
		return nil, fmt.Errorf("could not unpack same configuration file: %v", err)
	}

	sameConfig = &SameConfig{
		Spec: SameSpec{
			APIVersion: sameConfigFromFile.APIVersion,
			Version:    sameConfigFromFile.Version,
		},
	}

	sameConfig.Spec.Metadata = sameConfigFromFile.Metadata
	sameConfig.Spec.Bases = sameConfigFromFile.Bases
	sameConfig.Spec.EnvFiles = sameConfigFromFile.EnvFiles
	sameConfig.Spec.Resources = sameConfigFromFile.Resources
	sameConfig.Spec.Workflow.Parameters = sameConfigFromFile.Workflow.Parameters
	sameConfig.Spec.Pipeline = sameConfigFromFile.Pipeline
	sameConfig.Spec.DataSets = sameConfigFromFile.DataSets
	sameConfig.Spec.Run = sameConfigFromFile.Run
	sameConfig.Spec.ConfigFilePath = sameConfigFromFile.ConfigFilePath

	// a, _ := yaml.Marshal(sameConfig)
	// fmt.Println(string(a))

	return sameConfig, nil
}

// LoadSAMEConfig reads the samedef from a remote URI or local file,
// and returns the sameconfig.
func (v V1) LoadSAME(configFilePath string) (*SameConfig, error) {
	log.Trace("- In Root.LoadSAME")
	if configFilePath == "" {
		return &SameConfig{}, fmt.Errorf("config file must be the URI of a SameDef spec")
	}

	// Read contents
	log.Tracef("Config File Path: %v\n", configFilePath)
	resolvedConfigFilePath, err := netUrl.Parse(configFilePath)
	if err != nil {
		log.Errorf("root.go: could not resolve same config file path: %v", err)
		return &SameConfig{}, err
	}
	log.Tracef("Parsed file path to: %v\n", resolvedConfigFilePath.Path)
	configFileBytes, err := ioutil.ReadFile(resolvedConfigFilePath.Path)
	if err != nil {
		message := fmt.Errorf("root.go: could not read from config file %s: %v", configFilePath, err)
		log.Errorf(message.Error())
		return &SameConfig{}, message
	}

	log.Tracef("Loaded file into bytes of size: %v\n", len(configFileBytes))
	// Check API version.
	var obj map[string]interface{}
	if err = yaml.Unmarshal(configFileBytes, &obj); err != nil {
		return nil, fmt.Errorf("unable to unmarshal the yaml file - invalid config file format: %v", err)
	}

	log.Tracef("Unmarshalled bytes to yaml of size: %v\n", len(obj))

	sameconfig, err := v.unMarshallSAME(obj)
	if err != nil {
		log.Errorf("Failed to convert kfdef to kfconfig: %v", err)
		return &SameConfig{}, err
	}

	log.Trace("Loaded SAME")

	return sameconfig, nil
}
