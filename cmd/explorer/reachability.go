package main

import (
	"github.com/maomada/radar/internal/reachability"
	versionpkg "github.com/maomada/radar/internal/version"
)

func configureReachabilityImages() {
	reachability.DefaultImageRef = "ghcr.io/maomada/kubeatlas:" + versionpkg.Current
	reachability.LatestReleaseImage = nil
}
