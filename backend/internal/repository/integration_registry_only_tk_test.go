//go:build integration

package repository

import (
	"errors"
	"log"
	"os"
	"strings"
	"testing"
)

const integrationRegistryOnlyEnv = "SUB2API_TEST_REGISTRY_ONLY"

func runIntegrationRegistryOnlyIfRequested(m *testing.M) {
	requested, err := integrationRegistryOnlyRequested(
		os.Getenv(integrationRegistryOnlyEnv) == "1",
		os.Args[1:],
	)
	if err != nil {
		log.Printf("%v", err)
		os.Exit(2)
	}
	if requested {
		os.Exit(m.Run())
	}
}

func integrationRegistryOnlyRequested(
	enabled bool,
	arguments []string,
) (bool, error) {
	if !enabled {
		return false, nil
	}
	for _, argument := range arguments {
		if argument == "-test.list" || strings.HasPrefix(argument, "-test.list=") {
			return true, nil
		}
	}
	return false, errors.New("SUB2API_TEST_REGISTRY_ONLY requires -test.list")
}
