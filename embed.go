package main

import (
	"embed"
)

//go:generate go run embedgen.go

//go:embed frontend/dist.gz
var embedFS embed.FS
