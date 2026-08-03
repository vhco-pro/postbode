// DELETE AFTER P1 — see main.go and plan Phase 3/15 (F-07).
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// defaultConfigPath returns ~/.config/postbode/config.yaml (spec §6.5).
func defaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home directory: %w", err)
	}
	return filepath.Join(home, ".config", "postbode", "config.yaml"), nil
}

// writeCompanyNumberConfig writes companyNumber into path under
// administration.company_number (F-01, AC-1), preserving any other keys
// already present in the file (later phases add gmail/rules/vendors keys
// to the same file — the spike must not clobber them). The file is created
// with mode 0600 if it does not yet exist.
func writeCompanyNumberConfig(path, companyNumber string) error {
	if companyNumber == "" {
		return fmt.Errorf("writeCompanyNumberConfig: companyNumber is empty")
	}

	doc := yaml.Node{}
	existing, err := os.ReadFile(path)
	switch {
	case err == nil:
		if e := yaml.Unmarshal(existing, &doc); e != nil {
			return fmt.Errorf("writeCompanyNumberConfig: parse existing config %s: %w", path, e)
		}
	case os.IsNotExist(err):
		// No existing config — start from an empty mapping.
	default:
		return fmt.Errorf("writeCompanyNumberConfig: read existing config %s: %w", path, err)
	}

	root := asMapping(&doc)
	setNestedScalar(root, []string{"administration", "company_number"}, companyNumber)

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("writeCompanyNumberConfig: create config dir: %w", err)
	}

	out, err := yaml.Marshal(root)
	if err != nil {
		return fmt.Errorf("writeCompanyNumberConfig: encode config: %w", err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return fmt.Errorf("writeCompanyNumberConfig: write config %s: %w", path, err)
	}
	return nil
}

// asMapping returns doc's content as a *yaml.Node of kind MappingNode,
// creating an empty one if doc is the zero value (no existing file) or its
// document root isn't a mapping.
func asMapping(doc *yaml.Node) *yaml.Node {
	if doc.Kind == 0 {
		return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) == 1 && doc.Content[0].Kind == yaml.MappingNode {
		return doc.Content[0]
	}
	if doc.Kind == yaml.MappingNode {
		return doc
	}
	return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
}

// setNestedScalar sets root[path[0]][path[1]]...=value, creating
// intermediate mapping nodes as needed and overwriting a matching leaf key
// if one already exists, so re-running the spike updates the value in
// place rather than duplicating the key.
func setNestedScalar(root *yaml.Node, path []string, value string) {
	node := root
	for i, key := range path {
		leaf := i == len(path)-1

		var valueNode *yaml.Node
		for j := 0; j+1 < len(node.Content); j += 2 {
			if node.Content[j].Value == key {
				valueNode = node.Content[j+1]
				break
			}
		}

		if valueNode == nil {
			keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
			if leaf {
				valueNode = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
			} else {
				valueNode = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			}
			node.Content = append(node.Content, keyNode, valueNode)
		} else if leaf {
			valueNode.Kind = yaml.ScalarNode
			valueNode.Tag = "!!str"
			valueNode.Value = value
			valueNode.Content = nil
		}

		node = valueNode
	}
}
