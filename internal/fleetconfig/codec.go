package fleetconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"go.yaml.in/yaml/v3"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	maxBytes = 4 << 20
	maxDepth = 64
	maxNodes = 100000
)

// Encode renders the supported, portable part of a FleetConfig as YAML.
func Encode(config *hubv1.FleetConfig) ([]byte, error) {
	if config == nil || config.GetFormatVersion() != 1 {
		return nil, errors.New("fleet config: unsupported format version")
	}
	budget := limits{bytes: maxBytes, nodes: maxNodes}
	if err := checkMessage(config.ProtoReflect(), "config", 1, &budget); err != nil {
		return nil, err
	}
	jsonBytes, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(config)
	if err != nil {
		return nil, errors.New("fleet config: cannot encode protobuf")
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(jsonBytes))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, errors.New("fleet config: cannot encode JSON mapping")
	}
	remaining := maxNodes
	node, err := jsonToYAML(value, true, 1, &remaining)
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(node); err != nil {
		return nil, errors.New("fleet config: cannot encode YAML")
	}
	if err := encoder.Close(); err != nil {
		return nil, errors.New("fleet config: cannot finish YAML")
	}
	if output.Len() > maxBytes {
		return nil, errors.New("fleet config: document exceeds 4 MiB")
	}
	return output.Bytes(), nil
}

// Decode parses one bounded FleetConfig YAML document.
func Decode(input []byte) (*hubv1.FleetConfig, error) {
	if len(input) > maxBytes {
		return nil, errors.New("fleet config: document exceeds 4 MiB")
	}
	if len(bytes.TrimSpace(input)) == 0 {
		return nil, errors.New("fleet config: empty document")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(input))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, errors.New("fleet config: invalid YAML")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("fleet config: expected one document")
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("fleet config: expected a mapping")
	}
	remaining := maxNodes
	value, err := yamlToJSON(document.Content[0], (&hubv1.FleetConfig{}).ProtoReflect().Descriptor(), "config", 1, &remaining)
	if err != nil {
		return nil, err
	}
	version, ok := value["format_version"].(json.Number)
	if !ok || version.String() != "1" {
		return nil, errors.New("fleet config: unsupported format version")
	}
	jsonBytes, err := json.Marshal(value)
	if err != nil {
		return nil, errors.New("fleet config: invalid mapping")
	}
	var config hubv1.FleetConfig
	if err := (protojson.UnmarshalOptions{RecursionLimit: maxDepth}).Unmarshal(jsonBytes, &config); err != nil {
		return nil, errors.New("fleet config: invalid protobuf value")
	}
	if config.GetFormatVersion() != 1 {
		return nil, errors.New("fleet config: unsupported format version")
	}
	budget := limits{bytes: maxBytes, nodes: maxNodes}
	if err := checkMessage(config.ProtoReflect(), "config", 1, &budget); err != nil {
		return nil, err
	}
	if _, err := Encode(&config); err != nil {
		return nil, err
	}
	return &config, nil
}

func invalidAt(category, path string) error {
	if len(path) > 120 {
		path = path[:117] + "..."
	}
	return fmt.Errorf("fleet config: %s at %s", category, path)
}
