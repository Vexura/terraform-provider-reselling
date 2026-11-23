package provider

import (
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// registerSSHKeys adds the ssh-key endpoints to a fake API.
func registerSSHKeys(f *fakeAPI) map[string]map[string]any {
	keys := map[string]map[string]any{}
	f.handle("POST /products/rootserver/ssh-keys", func(r *fakeRequest) (int, any) {
		if r.Body["public_key"] == "invalid" {
			return validationFailed("public_key", "Der Schlüssel ist ungültig.")
		}
		key := map[string]any{
			"id": f.id(), "name": r.Body["name"], "public_key": r.Body["public_key"],
			"fingerprint": "SHA256:abc", "key_type": "ssh-ed25519", "is_default": r.Body["is_default"] == true,
		}
		keys[key["id"].(string)] = key
		return http.StatusCreated, map[string]any{"ssh_key": key}
	})
	f.handle("GET /products/rootserver/ssh-keys/{id}", func(r *fakeRequest) (int, any) {
		key, ok := keys[r.Params["id"]]
		if !ok {
			return notFound()
		}
		return http.StatusOK, map[string]any{"ssh_key": key}
	})
	f.handle("PATCH /products/rootserver/ssh-keys/{id}", func(r *fakeRequest) (int, any) {
		key, ok := keys[r.Params["id"]]
		if !ok {
			return notFound()
		}
		key["name"] = r.Body["name"]
		return http.StatusOK, map[string]any{"ssh_key": key}
	})
	f.handle("DELETE /products/rootserver/ssh-keys/{id}", func(r *fakeRequest) (int, any) {
		if _, ok := keys[r.Params["id"]]; !ok {
			return notFound()
		}
		delete(keys, r.Params["id"])
		return http.StatusOK, map[string]any{"deleted": true}
	})
	return keys
}

func TestSSHKeyResource(t *testing.T) {
	f := newFakeAPI(t)
	keys := registerSSHKeys(f)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: providerConfig(f) + `
resource "reselling_ssh_key" "admin" {
  name       = "admin"
  public_key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample admin@laptop"
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("reselling_ssh_key.admin", "id"),
					resource.TestCheckResourceAttr("reselling_ssh_key.admin", "fingerprint", "SHA256:abc"),
					resource.TestCheckResourceAttr("reselling_ssh_key.admin", "is_default", "false"),
				),
			},
			{
				Config: providerConfig(f) + `
resource "reselling_ssh_key" "admin" {
  name       = "renamed"
  public_key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample admin@laptop"
}`,
				Check: resource.TestCheckResourceAttr("reselling_ssh_key.admin", "name", "renamed"),
			},
			{
				ResourceName:      "reselling_ssh_key.admin",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
	if len(keys) != 0 {
		t.Fatalf("expected the key to be deleted, %d left", len(keys))
	}
	if f.called("PATCH /products/rootserver/ssh-keys/00000000-0000-4000-8000-000000000001") != 1 {
		t.Fatalf("expected exactly one rename call, calls: %v", f.calls)
	}
}

func TestSSHKeyValidationErrorIsShownOnAttribute(t *testing.T) {
	f := newFakeAPI(t)
	registerSSHKeys(f)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config: providerConfig(f) + `
resource "reselling_ssh_key" "bad" {
  name       = "bad"
  public_key = "invalid"
}`,
			ExpectError: regexpMust(`(?s)with reselling_ssh_key.bad,.*public_key.*Der Schlüssel ist ungültig`),
		}},
	})
}

func TestSSHKeyRemovedOutsideTerraformIsRecreated(t *testing.T) {
	f := newFakeAPI(t)
	keys := registerSSHKeys(f)
	config := providerConfig(f) + `
resource "reselling_ssh_key" "admin" {
  name       = "admin"
  public_key = "ssh-ed25519 AAAA admin"
}`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() {
					f.mu.Lock()
					for id := range keys {
						delete(keys, id)
					}
					f.mu.Unlock()
				},
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}
