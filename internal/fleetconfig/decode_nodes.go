package fleetconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"go.yaml.in/yaml/v3"
	"google.golang.org/protobuf/reflect/protoreflect"
)

var jsonNumber = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`)

func yamlToJSON(node *yaml.Node, descriptor protoreflect.MessageDescriptor, path string, depth int, remaining *int) (map[string]any, error) {
	if err := countNode(node, depth, remaining); err != nil {
		return nil, err
	}
	if node.Kind != yaml.MappingNode || node.Tag != "!!map" {
		return nil, invalidAt("expected mapping", path)
	}
	result := make(map[string]any, len(node.Content)/2)
	seen := make(map[protoreflect.FieldNumber]bool, len(node.Content)/2)
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if err := countNode(key, depth+1, remaining); err != nil {
			return nil, err
		}
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Anchor != "" {
			return nil, invalidAt("invalid field name", path)
		}
		field := descriptor.Fields().ByName(protoreflect.Name(key.Value))
		if field == nil {
			field = descriptor.Fields().ByJSONName(key.Value)
		}
		if field == nil {
			return nil, invalidAt("unknown field", path)
		}
		fieldPath := path + "." + string(field.Name())
		if seen[field.Number()] {
			return nil, invalidAt("duplicate field", fieldPath)
		}
		seen[field.Number()] = true
		if isForbiddenID(field.Message()) {
			return nil, invalidAt("hub ID is not portable", fieldPath)
		}
		converted, err := convertField(value, field, fieldPath, depth+1, remaining)
		if err != nil {
			return nil, err
		}
		result[string(field.Name())] = converted
	}
	return result, nil
}

func convertField(node *yaml.Node, field protoreflect.FieldDescriptor, path string, depth int, remaining *int) (any, error) {
	if node.Kind == yaml.ScalarNode && node.Tag == "!!null" && node.Value == "null" {
		if err := countNode(node, depth, remaining); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if field.IsList() {
		if err := countNode(node, depth, remaining); err != nil {
			return nil, err
		}
		if node.Kind != yaml.SequenceNode || node.Tag != "!!seq" {
			return nil, invalidAt("expected sequence", path)
		}
		items := make([]any, 0, len(node.Content))
		for i, item := range node.Content {
			converted, err := convertValue(item, field, fmt.Sprintf("%s[%d]", path, i), depth+1, remaining)
			if err != nil {
				return nil, err
			}
			items = append(items, converted)
		}
		return items, nil
	}
	if field.IsMap() {
		return nil, invalidAt("unsupported map", path)
	}
	return convertValue(node, field, path, depth, remaining)
}

func convertValue(node *yaml.Node, field protoreflect.FieldDescriptor, path string, depth int, remaining *int) (any, error) {
	if node.Kind == yaml.ScalarNode && node.Tag == "!!null" && node.Value == "null" {
		if err := countNode(node, depth, remaining); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if field.Kind() == protoreflect.MessageKind || field.Kind() == protoreflect.GroupKind {
		switch string(field.Message().FullName()) {
		case "google.protobuf.Duration", "google.protobuf.Timestamp", "google.protobuf.FieldMask":
			value, err := scalar(node, depth, remaining)
			if err != nil || value == nil {
				return nil, invalidAt("invalid well-known value", path)
			}
			if _, ok := value.(string); !ok {
				return nil, invalidAt("invalid well-known value", path)
			}
			return value, nil
		default:
			return yamlToJSON(node, field.Message(), path, depth, remaining)
		}
	}
	value, err := scalar(node, depth, remaining)
	if err != nil {
		return nil, invalidAt("invalid scalar", path)
	}
	if field.Kind() == protoreflect.EnumKind {
		name, ok := value.(string)
		if !ok || field.Enum().Values().ByName(protoreflect.Name(name)) == nil {
			return nil, invalidAt("unknown enum", path)
		}
	}
	return value, nil
}

func scalar(node *yaml.Node, depth int, remaining *int) (any, error) {
	if err := countNode(node, depth, remaining); err != nil {
		return nil, err
	}
	if node.Kind != yaml.ScalarNode {
		return nil, errors.New("invalid scalar")
	}
	switch node.Tag {
	case "!!str":
		return node.Value, nil
	case "!!bool":
		if node.Value == "true" {
			return true, nil
		}
		if node.Value == "false" {
			return false, nil
		}
	case "!!int", "!!float":
		if jsonNumber.MatchString(node.Value) {
			return json.Number(node.Value), nil
		}
	case "!!null":
		if node.Value == "null" {
			return nil, nil
		}
	}
	return nil, errors.New("invalid scalar")
}

func countNode(node *yaml.Node, depth int, remaining *int) error {
	if depth > maxDepth {
		return errors.New("fleet config: nesting exceeds 64 levels")
	}
	*remaining--
	if *remaining < 0 {
		return errors.New("fleet config: node count exceeds 100000")
	}
	if node.Anchor != "" || node.Kind == yaml.AliasNode {
		return errors.New("fleet config: anchors and aliases are not allowed")
	}
	return nil
}
