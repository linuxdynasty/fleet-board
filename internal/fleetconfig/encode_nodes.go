package fleetconfig

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

func jsonToYAML(value any, root bool, depth int, remaining *int) (*yaml.Node, error) {
	if depth > maxDepth {
		return nil, errors.New("fleet config: nesting exceeds 64 levels")
	}
	*remaining--
	if *remaining < 0 {
		return nil, errors.New("fleet config: node count exceeds 100000")
	}
	switch v := value.(type) {
	case map[string]any:
		node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		if len(v) > 0 && depth+1 > maxDepth {
			return nil, errors.New("fleet config: nesting exceeds 64 levels")
		}
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if root {
			for i, key := range keys {
				if key == "format_version" {
					keys = append([]string{key}, append(keys[:i], keys[i+1:]...)...)
					break
				}
			}
		}
		for _, key := range keys {
			*remaining--
			if *remaining < 0 {
				return nil, errors.New("fleet config: node count exceeds 100000")
			}
			child, err := jsonToYAML(v[key], false, depth+1, remaining)
			if err != nil {
				return nil, err
			}
			node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, child)
		}
		return node, nil
	case []any:
		node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, item := range v {
			child, err := jsonToYAML(item, false, depth+1, remaining)
			if err != nil {
				return nil, err
			}
			node.Content = append(node.Content, child)
		}
		return node, nil
	case string:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Style: yaml.DoubleQuotedStyle, Value: v}, nil
	case json.Number:
		tag := "!!int"
		if strings.ContainsAny(string(v), ".eE") {
			tag = "!!float"
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: string(v)}, nil
	case bool:
		if v {
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"}, nil
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "false"}, nil
	case nil:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}, nil
	default:
		return nil, errors.New("fleet config: unsupported JSON value")
	}
}
