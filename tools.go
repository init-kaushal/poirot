//go:build tools
// +build tools

package main

import (
	_ "k8s.io/api"
	_ "k8s.io/apimachinery"
	_ "k8s.io/client-go"
	_ "sigs.k8s.io/yaml"
)
