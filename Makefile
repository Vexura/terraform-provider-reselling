# Terraform provider for the reseller platform.
BINARY    := terraform-provider-reselling
VERSION   ?= 0.1.0
NAMESPACE ?= vexura
NAME      := reselling
OS_ARCH   := $(shell go env GOOS)_$(shell go env GOARCH)
PLUGINDIR := $(HOME)/.terraform.d/plugins/registry.terraform.io/$(NAMESPACE)/$(NAME)/$(VERSION)/$(OS_ARCH)
TFPLUGINDOCS := github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs@v0.24.0

.PHONY: build test testacc vet fmt install docs release-snapshot clean

build:
	go build -trimpath -ldflags "-X main.version=$(VERSION)" -o $(BINARY) .

# Unit tests run real Terraform against an in-memory fake of the API (Terraform is downloaded
# automatically when it is not on PATH). They never call the real API.
test: vet
	go test ./... -count=1

vet:
	go vet ./...

fmt:
	gofmt -w .
	terraform fmt -recursive examples

# Acceptance tests order real, billed resources. They refuse to run without TF_ACC=1 and a
# non-production RESELLING_ENDPOINT, see internal/provider/acceptance_test.go.
testacc:
	TF_ACC=1 go test ./internal/provider/ -run '^TestAcc' -count=1 -timeout 120m -v

# Installs the provider for local use as registry.terraform.io/$(NAMESPACE)/$(NAME) $(VERSION).
install: build
	mkdir -p $(PLUGINDIR)
	cp $(BINARY) $(PLUGINDIR)/$(BINARY)_v$(VERSION)

# Regenerates docs/ from the schema descriptions and examples/ (needs terraform on PATH).
docs:
	go run $(TFPLUGINDOCS) generate --provider-name $(NAME) --rendered-provider-name Reselling

release-snapshot:
	goreleaser release --snapshot --clean --skip=sign

clean:
	rm -rf $(BINARY) dist
