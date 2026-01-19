// Package xmlutil provides utilities for reading and modifying XML configuration files.
package xmlutil

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/beevik/etree"
)

// ConfigFile wraps an etree Document for manipulating XML config files.
// It provides a simple API for getting and setting element values.
type ConfigFile struct {
	doc  *etree.Document
	path string
	root *etree.Element
}

// Open reads an XML config file from disk. If the file doesn't exist,
// it creates a new document with the specified root element name.
func Open(path, rootElement string) (*ConfigFile, error) {
	doc := etree.NewDocument()

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// Create new document with root element
		root := doc.CreateElement(rootElement)
		return &ConfigFile{doc: doc, path: path, root: root}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", path, err)
	}

	if err := doc.ReadFromBytes(data); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", path, err)
	}

	root := doc.Root()
	if root == nil {
		return nil, fmt.Errorf("no root element in %s", path)
	}

	return &ConfigFile{doc: doc, path: path, root: root}, nil
}

// OpenExisting reads an XML config file from disk.
// Returns an error if the file doesn't exist.
func OpenExisting(path string) (*ConfigFile, error) {
	doc := etree.NewDocument()

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	if err := doc.ReadFromBytes(data); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", path, err)
	}

	root := doc.Root()
	if root == nil {
		return nil, fmt.Errorf("no root element in %s", path)
	}

	return &ConfigFile{doc: doc, path: path, root: root}, nil
}

// GetElement returns the text value of a child element, or empty string if not found.
func (c *ConfigFile) GetElement(name string) string {
	el := c.root.SelectElement(name)
	if el == nil {
		return ""
	}
	return el.Text()
}

// SetElement sets the value of a child element, creating it if it doesn't exist.
func (c *ConfigFile) SetElement(name, value string) {
	el := c.root.SelectElement(name)
	if el == nil {
		el = c.root.CreateElement(name)
	}
	el.SetText(value)
}

// SetStringArray sets a child element to contain multiple <string> children.
// This is used for .NET XML serialization format like:
//
//	<KnownProxies>
//	  <string>127.0.0.1</string>
//	  <string>::1</string>
//	</KnownProxies>
func (c *ConfigFile) SetStringArray(name string, values []string) {
	// Remove existing element if present
	if existing := c.root.SelectElement(name); existing != nil {
		c.root.RemoveChild(existing)
	}

	// Create new element with string children
	el := c.root.CreateElement(name)
	for _, v := range values {
		child := el.CreateElement("string")
		child.SetText(v)
	}
}

// Save writes the document back to disk with proper indentation.
func (c *ConfigFile) Save() error {
	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(c.path), 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	c.doc.Indent(2)
	return c.doc.WriteToFile(c.path)
}
