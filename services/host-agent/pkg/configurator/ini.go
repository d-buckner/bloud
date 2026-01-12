package configurator

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// INIFile represents a simple INI file that preserves formatting and comments
type INIFile struct {
	sections map[string]*INISection
	order    []string // Preserve section order
}

// INISection represents a section in an INI file
type INISection struct {
	name   string
	keys   map[string]string
	order  []string // Preserve key order
	lines  []string // Original lines for preservation
}

// NewINIFile creates a new empty INI file
func NewINIFile() *INIFile {
	return &INIFile{
		sections: make(map[string]*INISection),
	}
}

// LoadINI loads an INI file from disk
func LoadINI(path string) (*INIFile, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return NewINIFile(), nil
		}
		return nil, err
	}
	defer f.Close()

	ini := NewINIFile()
	var currentSection *INISection

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		// Section header
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			name := trimmed[1 : len(trimmed)-1]
			currentSection = &INISection{
				name:  name,
				keys:  make(map[string]string),
				lines: []string{line},
			}
			ini.sections[name] = currentSection
			ini.order = append(ini.order, name)
			continue
		}

		// Key=value
		if currentSection != nil && strings.Contains(trimmed, "=") {
			parts := strings.SplitN(trimmed, "=", 2)
			key := strings.TrimSpace(parts[0])
			value := ""
			if len(parts) > 1 {
				value = strings.TrimSpace(parts[1])
			}
			currentSection.keys[key] = value
			currentSection.order = append(currentSection.order, key)
		}

		// Preserve the line
		if currentSection != nil {
			currentSection.lines = append(currentSection.lines, line)
		}
	}

	return ini, scanner.Err()
}

// Section returns or creates a section
func (ini *INIFile) Section(name string) *INISection {
	if s, ok := ini.sections[name]; ok {
		return s
	}
	s := &INISection{
		name: name,
		keys: make(map[string]string),
	}
	ini.sections[name] = s
	ini.order = append(ini.order, name)
	return s
}

// Set sets a key in the section
func (s *INISection) Set(key, value string) {
	if _, exists := s.keys[key]; !exists {
		s.order = append(s.order, key)
	}
	s.keys[key] = value
}

// Get gets a key from the section
func (s *INISection) Get(key string) (string, bool) {
	v, ok := s.keys[key]
	return v, ok
}

// Save writes the INI file to disk
func (ini *INIFile) Save(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	for i, sectionName := range ini.order {
		section := ini.sections[sectionName]
		if i > 0 {
			fmt.Fprintln(f)
		}
		fmt.Fprintf(f, "[%s]\n", sectionName)
		for _, key := range section.order {
			value := section.keys[key]
			fmt.Fprintf(f, "%s=%s\n", key, value)
		}
	}

	return nil
}

// EnsureKeys ensures the specified keys exist with the specified values
// Returns true if any changes were made
func (ini *INIFile) EnsureKeys(sectionName string, keys map[string]string) bool {
	section := ini.Section(sectionName)
	changed := false
	for key, value := range keys {
		existing, ok := section.Get(key)
		if !ok || existing != value {
			section.Set(key, value)
			changed = true
		}
	}
	return changed
}
