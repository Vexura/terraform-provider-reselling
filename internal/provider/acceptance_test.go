package provider

import (
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/vexura/terraform-provider-reselling/internal/client"
)

// Acceptance tests create real, billed resources. They only run with TF_ACC=1 and an explicit
// non-production endpoint (RESELLING_ENDPOINT), so that a stray `go test` never orders servers
// on the production platform.
func accPreCheck(t *testing.T, required ...string) {
	t.Helper()
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance tests need TF_ACC=1")
	}
	endpoint := os.Getenv(EndpointEnv)
	if endpoint == "" || endpoint == client.DefaultEndpoint {
		t.Skip("acceptance tests need " + EndpointEnv + " pointing at a staging API, never production")
	}
	for _, name := range append([]string{APIKeyEnv}, required...) {
		if os.Getenv(name) == "" {
			t.Skipf("acceptance test needs %s", name)
		}
	}
}

func TestAccSSHKey(t *testing.T) {
	accPreCheck(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
resource "reselling_ssh_key" "acc" {
  name       = "tf-acc"
  public_key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGtmYWtla2V5Zm9yYWNjZXB0YW5jZXRlc3RzMDAwMA tf-acc"
}`,
				Check: resource.TestCheckResourceAttrSet("reselling_ssh_key.acc", "fingerprint"),
			},
			{ResourceName: "reselling_ssh_key.acc", ImportState: true, ImportStateVerify: true},
		},
	})
}

func TestAccRootserverWithVolume(t *testing.T) {
	accPreCheck(t, "RESELLING_ACC_CLUSTER_ID", "RESELLING_ACC_OS_VERSION_ID", "RESELLING_ACC_STORAGE_CLASS_ID")
	config := fmt.Sprintf(`
resource "reselling_rootserver" "acc" {
  cluster_id    = %q
  os_version_id = %q
  hostname      = "tf-acc.example.com"
  cores         = 1
  ram           = 1024
  disk          = 20
}

resource "reselling_rootserver_volume" "acc" {
  name             = "tf-acc"
  storage_class_id = %q
  size_gb          = 10
}

resource "reselling_rootserver_volume_attachment" "acc" {
  volume_id = reselling_rootserver_volume.acc.id
  server_id = reselling_rootserver.acc.id
}`, os.Getenv("RESELLING_ACC_CLUSTER_ID"), os.Getenv("RESELLING_ACC_OS_VERSION_ID"), os.Getenv("RESELLING_ACC_STORAGE_CLASS_ID"))
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("reselling_rootserver.acc", "status", "running"),
					resource.TestCheckResourceAttrSet("reselling_rootserver_volume_attachment.acc", "guest_device_path"),
				),
			},
		},
	})
}
