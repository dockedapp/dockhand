package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// UpdateOperationVersion updates the current_version field of a named operation
// in the YAML config file at path. It uses the yaml.Node API to preserve
// comments and key ordering. The write is atomic via a .tmp file + rename.
func UpdateOperationVersion(path, opName, newVersion string) error {
	if path == "" {
		return nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading config for version update: %w", err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parsing config for version update: %w", err)
	}

	// doc is a Document node; content[0] is the root mapping
	if len(doc.Content) == 0 {
		return fmt.Errorf("empty config document")
	}
	root := doc.Content[0]

	// Navigate: root → "operations"
	opsNode := mappingGet(root, "operations")
	if opsNode == nil {
		return fmt.Errorf("no 'operations' key in config")
	}

	// Navigate: opsNode → opName
	opNode := mappingGet(opsNode, opName)
	if opNode == nil {
		return fmt.Errorf("operation %q not found in config", opName)
	}

	// Find or create "current_version" key inside opNode
	cvNode := mappingGet(opNode, "current_version")
	if cvNode != nil {
		// Update in place
		cvNode.Value = newVersion
	} else {
		// Append key + value nodes
		keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "current_version"}
		valNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: newVersion}
		opNode.Content = append(opNode.Content, keyNode, valNode)
	}

	out, err := yaml.Marshal(&doc)
	if err != nil {
		return fmt.Errorf("marshaling updated config: %w", err)
	}

	// Atomic write
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0600); err != nil {
		return fmt.Errorf("writing temp config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("renaming temp config: %w", err)
	}

	return nil
}

// UpdateAppVersion updates the current_version field of a named app in the
// YAML config file at path. Uses the same yaml.Node API as UpdateOperationVersion.
func UpdateAppVersion(path, appName, newVersion string) error {
	if path == "" {
		return nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading config for app version update: %w", err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parsing config for app version update: %w", err)
	}

	if len(doc.Content) == 0 {
		return fmt.Errorf("empty config document")
	}
	root := doc.Content[0]

	appsNode := mappingGet(root, "apps")
	if appsNode == nil {
		return fmt.Errorf("no 'apps' key in config")
	}

	appNode := mappingGet(appsNode, appName)
	if appNode == nil {
		return fmt.Errorf("app %q not found in config", appName)
	}

	cvNode := mappingGet(appNode, "current_version")
	if cvNode != nil {
		cvNode.Value = newVersion
	} else {
		keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "current_version"}
		valNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: newVersion}
		appNode.Content = append(appNode.Content, keyNode, valNode)
	}

	out, err := yaml.Marshal(&doc)
	if err != nil {
		return fmt.Errorf("marshaling updated config: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0600); err != nil {
		return fmt.Errorf("writing temp config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("renaming temp config: %w", err)
	}

	return nil
}

// mappingGet returns the value node for the given key in a YAML MappingNode,
// or nil if not found.
func mappingGet(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}
