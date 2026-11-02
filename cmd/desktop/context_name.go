package main

import "github.com/maomada/radar/internal/contextname"

func clusterShortName(contextName string) string {
	return contextname.ShortName(contextName)
}
