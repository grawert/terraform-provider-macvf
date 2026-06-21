package main

import (
	"context"
	"flag"
	"log"

	"terraform-provider-macvf/internal/provider"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
)

// These vars are set at build time via -ldflags.
var (
	version           = "dev"
	providerName      = "default"
	providerNamespace = "default"
)

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "set to true to run the provider with support for debuggers like delve")
	flag.Parse()

	address := "registry.terraform.io/" + providerNamespace + "/" + providerName
	err := providerserver.Serve(context.Background(), provider.New(version, providerName), providerserver.ServeOpts{
		Address: address,
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}
