package main

import (
	"embed"
)

//go:embed frontend/dist
var embedFS embed.FS
