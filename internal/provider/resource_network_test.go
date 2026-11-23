package provider

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestStartupScriptResource(t *testing.T) {
	f := newFakeAPI(t)
	scripts := map[string]map[string]any{}
	f.handle("POST /products/rootserver/startup-scripts", func(r *fakeRequest) (int, any) {
		s := map[string]any{"id": f.id(), "name": r.Body["name"], "type": r.Body["type"], "script": r.Body["script"], "is_default": len(scripts) == 0}
		scripts[s["id"].(string)] = s
		return http.StatusCreated, map[string]any{"startup_script": s}
	})
	f.handle("GET /products/rootserver/startup-scripts/{id}", func(r *fakeRequest) (int, any) {
		s, ok := scripts[r.Params["id"]]
		if !ok {
			return notFound()
		}
		return http.StatusOK, map[string]any{"startup_script": s}
	})
	f.handle("PATCH /products/rootserver/startup-scripts/{id}", func(r *fakeRequest) (int, any) {
		s := scripts[r.Params["id"]]
		merge(s, r.Body, "name", "type", "script")
		return http.StatusOK, map[string]any{"startup_script": s}
	})
	f.handle("DELETE /products/rootserver/startup-scripts/{id}", func(r *fakeRequest) (int, any) {
		delete(scripts, r.Params["id"])
		return http.StatusOK, map[string]any{"deleted": true}
	})
	config := func(script string) string {
		return providerConfig(f) + fmt.Sprintf(`
resource "reselling_startup_script" "docker" {
  name   = "docker"
  type   = "bash"
  script = %q
}`, script)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{Config: config("#!/bin/bash\napt-get install -y docker.io"), Check: resource.TestCheckResourceAttr("reselling_startup_script.docker", "is_default", "true")},
			{Config: config("#!/bin/bash\necho changed"), Check: resource.TestCheckResourceAttr("reselling_startup_script.docker", "script", "#!/bin/bash\necho changed")},
			{ResourceName: "reselling_startup_script.docker", ImportState: true, ImportStateVerify: true},
		},
	})
	if len(scripts) != 0 {
		t.Fatal("script not deleted")
	}
}

func TestVolumeAndAttachment(t *testing.T) {
	f := newFakeAPI(t)
	volumes := map[string]map[string]any{}
	reads := map[string]int{}
	f.handle("POST /products/rootserver/volumes", func(r *fakeRequest) (int, any) {
		v := map[string]any{
			"id": f.id(), "name": r.Body["name"], "size_gb": r.Body["size_gb"], "status": "creating",
			"storage_class": map[string]any{"id": r.Body["storage_class_id"]}, "cluster_id": "cluster-1",
			"location": map[string]any{"id": "loc-1"}, "server": nil,
		}
		volumes[v["id"].(string)] = v
		return http.StatusAccepted, map[string]any{"volume": v}
	})
	f.handle("GET /products/rootserver/volumes/{id}", func(r *fakeRequest) (int, any) {
		v, ok := volumes[r.Params["id"]]
		if !ok {
			return notFound()
		}
		reads[r.Params["id"]]++
		// Every transitional state settles on the next read.
		switch v["status"] {
		case "creating", "detaching":
			v["status"] = "available"
			v["server"] = nil
			v["device"] = nil
		case "attaching", "resizing":
			v["status"] = "attached"
		case "deleting":
			delete(volumes, r.Params["id"])
			return notFound()
		}
		return http.StatusOK, map[string]any{"volume": v}
	})
	f.handle("PATCH /products/rootserver/volumes/{id}", func(r *fakeRequest) (int, any) {
		merge(volumes[r.Params["id"]], r.Body, "name")
		return http.StatusOK, map[string]any{"volume": volumes[r.Params["id"]]}
	})
	f.handle("POST /products/rootserver/volumes/{id}/attach", func(r *fakeRequest) (int, any) {
		v := volumes[r.Params["id"]]
		v["status"] = "attaching"
		v["server"] = map[string]any{"id": r.Body["server_id"], "host_name": "web"}
		v["device"] = "scsi1"
		v["guest_device_path"] = "/dev/disk/by-id/scsi-0QEMU_QEMU_HARDDISK_drive-scsi1"
		return http.StatusAccepted, map[string]any{"volume": v, "task": nil}
	})
	f.handle("POST /products/rootserver/volumes/{id}/detach", func(r *fakeRequest) (int, any) {
		volumes[r.Params["id"]]["status"] = "detaching"
		return http.StatusAccepted, map[string]any{"volume": volumes[r.Params["id"]], "task": nil}
	})
	f.handle("POST /products/rootserver/volumes/{id}/resize", func(r *fakeRequest) (int, any) {
		v := volumes[r.Params["id"]]
		if v["server"] == nil {
			return http.StatusConflict, map[string]any{"success": false, "error": map[string]any{"code": "volume_not_attached", "message": "nur verbunden"}}
		}
		v["status"] = "resizing"
		v["size_gb"] = r.Body["size_gb"]
		return http.StatusAccepted, map[string]any{"volume": v, "task": nil}
	})
	f.handle("DELETE /products/rootserver/volumes/{id}", func(r *fakeRequest) (int, any) {
		v, ok := volumes[r.Params["id"]]
		if !ok {
			return notFound()
		}
		if v["server"] != nil {
			return http.StatusConflict, map[string]any{"success": false, "error": map[string]any{"code": "volume_attached", "message": "erst trennen"}}
		}
		v["status"] = "deleting"
		return http.StatusAccepted, map[string]any{"volume": v, "task": nil}
	})

	config := func(size int) string {
		return providerConfig(f) + fmt.Sprintf(`
resource "reselling_rootserver_volume" "data" {
  name             = "data"
  storage_class_id = "sc-ssd"
  size_gb          = %d
}

resource "reselling_rootserver_volume_attachment" "data" {
  volume_id = reselling_rootserver_volume.data.id
  server_id = "server-1"
}`, size)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config(50),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("reselling_rootserver_volume.data", "status", "available"),
					resource.TestCheckResourceAttr("reselling_rootserver_volume.data", "cluster_id", "cluster-1"),
					resource.TestCheckResourceAttr("reselling_rootserver_volume_attachment.data", "device", "scsi1"),
				),
			},
			{
				Config: config(80),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("reselling_rootserver_volume.data", "size_gb", "80"),
					resource.TestCheckResourceAttr("reselling_rootserver_volume.data", "server_id", "server-1"),
				),
			},
			{ResourceName: "reselling_rootserver_volume.data", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts", "status"}},
			{ResourceName: "reselling_rootserver_volume_attachment.data", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"}},
		},
	})
	if len(volumes) != 0 {
		t.Fatalf("volume not deleted: %v", volumes)
	}
}

func TestVPCRouteAndMember(t *testing.T) {
	f := newFakeAPI(t)
	tasks := registerTasks(f)
	vpcs := map[string]map[string]any{}
	routes := map[string]map[string]any{}
	members := map[string]map[string]any{}

	f.handle("POST /products/rootserver/vpc/create", func(r *fakeRequest) (int, any) {
		if r.Body["subnet"] == "10.100.0.0/24" {
			return http.StatusUnprocessableEntity, map[string]any{"success": false, "error": map[string]any{"code": "subnet_reserved", "message": "Dieses Netz nutzt die Plattform.", "field": "subnet"}}
		}
		v := map[string]any{"id": f.id(), "name": r.Body["name"], "description": r.Body["description"], "subnet": r.Body["subnet"], "location_id": r.Body["location_id"], "gateway": "10.20.0.1", "prefix_length": 24}
		vpcs[v["id"].(string)] = v
		return http.StatusCreated, map[string]any{"vpc": v}
	})
	f.handle("GET /products/rootserver/vpc/{id}", func(r *fakeRequest) (int, any) {
		v, ok := vpcs[r.Params["id"]]
		if !ok {
			return notFound()
		}
		return http.StatusOK, v
	})
	f.handle("POST /products/rootserver/vpc/{id}/update", func(r *fakeRequest) (int, any) {
		merge(vpcs[r.Params["id"]], r.Body, "name", "description")
		return http.StatusOK, map[string]any{"vpc": vpcs[r.Params["id"]]}
	})
	f.handle("POST /products/rootserver/vpc/{id}/delete", func(r *fakeRequest) (int, any) {
		if len(members) > 0 {
			return http.StatusUnprocessableEntity, map[string]any{"success": false, "error": map[string]any{"code": "vpc_not_empty", "message": "Mitglieder zuerst entfernen"}}
		}
		delete(vpcs, r.Params["id"])
		return http.StatusOK, map[string]any{"message": "VPC gelöscht"}
	})
	f.handle("POST /products/rootserver/vpc/{id}/link", func(r *fakeRequest) (int, any) {
		ip := "10.20.0.2"
		if v, ok := r.Body["private_ip"].(string); ok {
			ip = v
		}
		members[r.Body["rootserver_id"].(string)] = map[string]any{
			"id": f.id(), "member_type": "rootserver", "member_id": r.Body["rootserver_id"], "status": "active",
			"private_ip": ip, "nic": "net1", "guest_interface": "ens19",
		}
		return http.StatusAccepted, map[string]any{"task": tasks.start(f), "private_ip": ip}
	})
	f.handle("POST /products/rootserver/vpc/{id}/unlink", func(r *fakeRequest) (int, any) {
		delete(members, r.Body["rootserver_id"].(string))
		return http.StatusAccepted, map[string]any{"task": tasks.start(f)}
	})
	f.handle("GET /products/rootserver/vpc/{id}/members", func(r *fakeRequest) (int, any) {
		list := []any{}
		for _, m := range members {
			list = append(list, m)
		}
		return http.StatusOK, map[string]any{"members": list}
	})
	f.handle("POST /products/rootserver/vpc/{id}/routes", func(r *fakeRequest) (int, any) {
		rt := map[string]any{"id": f.id(), "destination": r.Body["destination"], "next_hop": r.Body["next_hop"], "description": r.Body["description"]}
		routes[rt["id"].(string)] = rt
		return http.StatusCreated, map[string]any{"route": rt}
	})
	f.handle("GET /products/rootserver/vpc/{id}/routes", func(r *fakeRequest) (int, any) {
		list := []any{}
		for _, rt := range routes {
			list = append(list, rt)
		}
		return http.StatusOK, map[string]any{"routes": list}
	})
	f.handle("POST /products/rootserver/vpc/{id}/routes/{route}/update", func(r *fakeRequest) (int, any) {
		merge(routes[r.Params["route"]], r.Body, "destination", "next_hop", "description")
		return http.StatusOK, map[string]any{"route": routes[r.Params["route"]]}
	})
	f.handle("POST /products/rootserver/vpc/{id}/routes/{route}/delete", func(r *fakeRequest) (int, any) {
		delete(routes, r.Params["route"])
		return http.StatusOK, map[string]any{"message": "Route gelöscht"}
	})

	config := func(name, destination string) string {
		return providerConfig(f) + fmt.Sprintf(`
resource "reselling_vpc" "backend" {
  name        = %q
  subnet      = "10.20.0.0/24"
  location_id = "loc-1"
}

resource "reselling_vpc_member" "router" {
  vpc_id     = reselling_vpc.backend.id
  server_id  = "server-1"
  private_ip = "10.20.0.5"
}

resource "reselling_vpc_route" "office" {
  vpc_id      = reselling_vpc.backend.id
  destination = %q
  next_hop    = reselling_vpc_member.router.private_ip
}`, name, destination)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: providerConfig(f) + `
resource "reselling_vpc" "bad" {
  name        = "bad"
  subnet      = "10.100.0.0/24"
  location_id = "loc-1"
}`,
				ExpectError: regexpMust(`(?s)reselling_vpc.bad.*subnet.*Dieses Netz nutzt die Plattform`),
			},
			{
				Config: config("backend", "192.168.10.0/24"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("reselling_vpc.backend", "gateway", "10.20.0.1"),
					resource.TestCheckResourceAttr("reselling_vpc_member.router", "nic", "net1"),
					resource.TestCheckResourceAttr("reselling_vpc_route.office", "next_hop", "10.20.0.5"),
				),
			},
			{
				Config: config("backend-renamed", "192.168.20.0/24"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("reselling_vpc.backend", "name", "backend-renamed"),
					resource.TestCheckResourceAttr("reselling_vpc_route.office", "destination", "192.168.20.0/24"),
				),
			},
			{ResourceName: "reselling_vpc.backend", ImportState: true, ImportStateVerify: true},
			{
				ResourceName: "reselling_vpc_route.office", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: childImportID("reselling_vpc_route.office", "vpc_id"),
			},
			{
				ResourceName: "reselling_vpc_member.router", ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"timeouts"},
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					return s.RootModule().Resources["reselling_vpc_member.router"].Primary.ID, nil
				},
			},
		},
	})
	if len(vpcs)+len(routes)+len(members) != 0 {
		t.Fatalf("left over: %d vpcs, %d routes, %d members", len(vpcs), len(routes), len(members))
	}
}

// childImportID builds "parent_id/id" for import steps.
func childImportID(name, parentAttr string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[name]
		if !ok {
			return "", fmt.Errorf("%s not in state", name)
		}
		return rs.Primary.Attributes[parentAttr] + "/" + rs.Primary.ID, nil
	}
}
