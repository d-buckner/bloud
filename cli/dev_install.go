// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"fmt"
	"strings"
)

func cmdInstall(args []string) int {
	if len(args) < 1 {
		errorf("Usage: ./bloud install <app-name>")
		return 1
	}
	return installApp(3000, args[0])
}

func cmdUninstall(args []string) int {
	if len(args) < 1 {
		errorf("Usage: ./bloud uninstall <app-name>")
		return 1
	}
	return uninstallApp(3000, args[0])
}

// installApp calls the host-agent API to install an app
func installApp(apiPort int, appName string) int {
	log(fmt.Sprintf("Installing %s...", appName))

	token, err := readAPIToken(context.Background())
	if err != nil {
		errorf("Could not read the host-agent API token: %v", err)
		return 1
	}

	curlCmd := fmt.Sprintf(`curl -s -X POST %s -w "\n%%{http_code}" http://localhost:%d/api/apps/%s/install`,
		authHeader(token), apiPort, appName)
	output, err := LocalExec(curlCmd)
	if err != nil {
		errorf("Failed to call install API: %v", err)
		return 1
	}

	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 1 {
		errorf("Empty response from API")
		return 1
	}

	httpCode := lines[len(lines)-1]
	responseBody := strings.Join(lines[:len(lines)-1], "\n")

	if httpCode != "200" && httpCode != "201" && httpCode != "202" {
		errorf("Install failed (HTTP %s): %s", httpCode, responseBody)
		return 1
	}

	log(fmt.Sprintf("Successfully installed %s", appName))
	fmt.Println(responseBody)
	return 0
}

// uninstallApp calls the host-agent API to uninstall an app
func uninstallApp(apiPort int, appName string) int {
	log(fmt.Sprintf("Uninstalling %s...", appName))

	token, err := readAPIToken(context.Background())
	if err != nil {
		errorf("Could not read the host-agent API token: %v", err)
		return 1
	}

	curlCmd := fmt.Sprintf(`curl -s -X POST %s -w "\n%%{http_code}" http://localhost:%d/api/apps/%s/uninstall`,
		authHeader(token), apiPort, appName)
	output, err := LocalExec(curlCmd)
	if err != nil {
		errorf("Failed to call uninstall API: %v", err)
		return 1
	}

	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 1 {
		errorf("Empty response from API")
		return 1
	}

	httpCode := lines[len(lines)-1]
	responseBody := strings.Join(lines[:len(lines)-1], "\n")

	if httpCode != "200" && httpCode != "202" {
		errorf("Uninstall failed (HTTP %s): %s", httpCode, responseBody)
		return 1
	}

	log(fmt.Sprintf("Successfully uninstalled %s", appName))
	fmt.Println(responseBody)
	return 0
}
