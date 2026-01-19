package xmlutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpen_NewFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.xml")

	cfg, err := Open(path, "Config")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	cfg.SetElement("TestKey", "TestValue")
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Verify file was created
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	if !strings.Contains(string(content), "<TestKey>TestValue</TestKey>") {
		t.Errorf("Expected file to contain <TestKey>TestValue</TestKey>, got: %s", content)
	}
}

func TestOpen_ExistingFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.xml")

	// Create initial file
	initial := `<?xml version="1.0" encoding="UTF-8"?>
<Config>
  <ExistingKey>ExistingValue</ExistingKey>
</Config>`
	if err := os.WriteFile(path, []byte(initial), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := Open(path, "Config")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	// Verify existing value can be read
	if got := cfg.GetElement("ExistingKey"); got != "ExistingValue" {
		t.Errorf("GetElement() = %q, want %q", got, "ExistingValue")
	}
}

func TestOpenExisting_FileNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "nonexistent.xml")

	_, err := OpenExisting(path)
	if err == nil {
		t.Error("OpenExisting() expected error for nonexistent file")
	}
}

func TestSetElement_CreateAndUpdate(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.xml")

	cfg, err := Open(path, "Config")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	// Create new element
	cfg.SetElement("NewKey", "NewValue")
	if got := cfg.GetElement("NewKey"); got != "NewValue" {
		t.Errorf("GetElement() after create = %q, want %q", got, "NewValue")
	}

	// Update existing element
	cfg.SetElement("NewKey", "UpdatedValue")
	if got := cfg.GetElement("NewKey"); got != "UpdatedValue" {
		t.Errorf("GetElement() after update = %q, want %q", got, "UpdatedValue")
	}
}

func TestGetElement_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.xml")

	cfg, err := Open(path, "Config")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	if got := cfg.GetElement("NonExistent"); got != "" {
		t.Errorf("GetElement() for nonexistent = %q, want empty string", got)
	}
}

func TestSetStringArray(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.xml")

	cfg, err := Open(path, "Config")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	// Set array values
	cfg.SetStringArray("KnownProxies", []string{"127.0.0.1", "::1", "192.168.1.1"})

	if err := cfg.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Verify file content
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	contentStr := string(content)

	// Check structure
	if !strings.Contains(contentStr, "<KnownProxies>") {
		t.Error("Expected <KnownProxies> element")
	}
	if !strings.Contains(contentStr, "<string>127.0.0.1</string>") {
		t.Error("Expected <string>127.0.0.1</string>")
	}
	if !strings.Contains(contentStr, "<string>::1</string>") {
		t.Error("Expected <string>::1</string>")
	}
	if !strings.Contains(contentStr, "<string>192.168.1.1</string>") {
		t.Error("Expected <string>192.168.1.1</string>")
	}
}

func TestSetStringArray_ReplacesExisting(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.xml")

	// Create file with existing array
	initial := `<?xml version="1.0" encoding="UTF-8"?>
<Config>
  <KnownProxies>
    <string>old-value</string>
  </KnownProxies>
</Config>`
	if err := os.WriteFile(path, []byte(initial), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := Open(path, "Config")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	// Replace with new values
	cfg.SetStringArray("KnownProxies", []string{"new-value-1", "new-value-2"})

	if err := cfg.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Verify old value is gone and new values are present
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	contentStr := string(content)

	if strings.Contains(contentStr, "old-value") {
		t.Error("Expected old-value to be removed")
	}
	if !strings.Contains(contentStr, "<string>new-value-1</string>") {
		t.Error("Expected <string>new-value-1</string>")
	}
	if !strings.Contains(contentStr, "<string>new-value-2</string>") {
		t.Error("Expected <string>new-value-2</string>")
	}
}

func TestSetStringArray_EmptyArray(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.xml")

	cfg, err := Open(path, "Config")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	// Set empty array
	cfg.SetStringArray("EmptyArray", []string{})

	if err := cfg.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Verify element exists but is empty
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	if !strings.Contains(string(content), "<EmptyArray/>") && !strings.Contains(string(content), "<EmptyArray></EmptyArray>") {
		t.Errorf("Expected empty EmptyArray element, got: %s", content)
	}
}
