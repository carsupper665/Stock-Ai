package main

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var versionFile string

// Version is the first line of VERSION. The lines after it list the versions this service
// depends on; scripts/check_versions.py compares them when the images are built.
var Version = strings.TrimSpace(strings.SplitN(versionFile, "\n", 2)[0])
