// Command terraform-provider-reselling is the Terraform provider for the reseller platform.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/vexura/terraform-provider-reselling/internal/provider"
)

// version is set by goreleaser through -ldflags "-X main.version=...".
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run the provider with support for debuggers such as delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/vexura/reselling",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}
