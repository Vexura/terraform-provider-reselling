package provider

import (
	"fmt"
	"net/http"
	"sort"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestKubernetesClusterAddonAndKubeconfig(t *testing.T) {
	f := newFakeAPI(t)
	clusters := map[string]map[string]any{}
	pools := map[string]map[string]any{}
	addons := map[string]map[string]any{}
	reads := map[string]int{}
	upgrades := 0

	view := func(id string) map[string]any {
		c := copyMap(clusters[id])
		list := []any{}
		ids := []string{}
		for pid := range pools {
			ids = append(ids, pid)
		}
		sort.Strings(ids)
		for _, pid := range ids {
			if pools[pid]["_cluster"] == id {
				p := copyMap(pools[pid])
				delete(p, "_cluster")
				list = append(list, p)
			}
		}
		c["node_pools"] = list
		c["version"] = map[string]any{"id": c["_version_id"], "version": "1.31." + fmt.Sprint(upgrades)}
		c["location"] = map[string]any{"id": "loc-1", "name": "Frankfurt"}
		delete(c, "_version_id")
		return c
	}
	addPool := func(clusterID string, body map[string]any, controlPlane bool) {
		p := map[string]any{
			"id": f.id(), "_cluster": clusterID, "name": body["name"], "is_control_plane": controlPlane,
			"plan":                   map[string]any{"id": body["plan_id"]},
			"infrastructure_cluster": map[string]any{"id": body["infrastructure_cluster_id"]},
			"min_nodes":              body["min_nodes"], "max_nodes": body["max_nodes"], "auto_scale": body["auto_scale"] == true,
			"node_count": body["min_nodes"], "label_key": body["label_key"], "label_value": body["label_value"],
		}
		pools[p["id"].(string)] = p
	}

	f.handle("POST /products/kubernetes/create", func(r *fakeRequest) (int, any) {
		id := f.id()
		clusters[id] = map[string]any{
			"id": id, "name": r.Body["name"], "status": "pending", "_version_id": r.Body["version_id"], "control_plane_count": r.Body["control_plane_count"],
			"vpc_id": "vpc-k8s", "pod_cidr": "10.244.0.0/16", "service_cidr": "10.96.0.0/12", "auto_upgrade": r.Body["auto_upgrade"],
			"endpoint": "https://k8s.example.net:6443", "maintenance_window": map[string]any{"day": nil, "hour": 3},
		}
		addPool(id, map[string]any{"name": "control-plane", "plan_id": r.Body["control_plane_plan_id"], "infrastructure_cluster_id": r.Body["cluster_id"], "min_nodes": 1, "max_nodes": 1}, true)
		for _, wp := range r.Body["worker_pools"].([]any) {
			addPool(id, wp.(map[string]any), false)
		}
		c := copyMap(clusters[id])
		c["version"] = "1.31.0"
		c["location"] = "Frankfurt"
		delete(c, "_version_id")
		return http.StatusAccepted, map[string]any{"cluster": c}
	})
	f.handle("GET /products/kubernetes/{id}", func(r *fakeRequest) (int, any) {
		c, ok := clusters[r.Params["id"]]
		if !ok {
			return notFound()
		}
		reads[r.Params["id"]]++
		if c["status"] != "running" && c["status"] != "pending_deletion" {
			c["status"] = "running"
			return http.StatusOK, map[string]any{"id": c["id"], "name": c["name"], "status": "provisioning"}
		}
		return http.StatusOK, view(r.Params["id"])
	})
	f.handle("POST /products/kubernetes/{id}/update", func(r *fakeRequest) (int, any) {
		merge(clusters[r.Params["id"]], r.Body, "name", "auto_upgrade")
		return http.StatusOK, map[string]any{"cluster": map[string]any{"id": r.Params["id"]}}
	})
	f.handle("POST /products/kubernetes/{id}/upgrade", func(r *fakeRequest) (int, any) {
		upgrades++
		c := clusters[r.Params["id"]]
		c["_version_id"] = r.Body["version_id"]
		c["status"] = "upgrading"
		return http.StatusAccepted, map[string]any{"message": "Versionsupgrade gestartet"}
	})
	f.handle("POST /products/kubernetes/{id}/delete", func(r *fakeRequest) (int, any) {
		clusters[r.Params["id"]]["status"] = "pending_deletion"
		return http.StatusOK, map[string]any{"message": "Cluster zur Loeschung markiert"}
	})
	f.handle("POST /products/kubernetes/{id}/node-pools/create", func(r *fakeRequest) (int, any) {
		addPool(r.Params["id"], r.Body, false)
		return http.StatusAccepted, map[string]any{"node_pool": map[string]any{}}
	})
	f.handle("POST /products/kubernetes/{id}/node-pools/{pool}/update", func(r *fakeRequest) (int, any) {
		merge(pools[r.Params["pool"]], r.Body, "min_nodes", "max_nodes", "auto_scale", "label_key", "label_value")
		return http.StatusOK, map[string]any{"node_pool": pools[r.Params["pool"]]}
	})
	f.handle("POST /products/kubernetes/{id}/node-pools/{pool}/delete", func(r *fakeRequest) (int, any) {
		delete(pools, r.Params["pool"])
		return http.StatusOK, map[string]any{"message": "Node Pool wird geloescht"}
	})
	f.handle("GET /products/kubernetes/{id}/kubeconfig", func(r *fakeRequest) (int, any) {
		return http.StatusOK, map[string]any{"kubeconfig": "apiVersion: v1\nkind: Config\n"}
	})
	f.handle("POST /products/kubernetes/{id}/addons/install", func(r *fakeRequest) (int, any) {
		a := map[string]any{"id": f.id(), "slug": r.Body["addon"], "status": "installing", "version": "1.15.0", "values": r.Body["values"]}
		addons[a["id"].(string)] = a
		dep := map[string]any{"id": f.id(), "slug": "dependency", "status": "installing", "version": "1.0.0", "values": map[string]any{}}
		return http.StatusAccepted, map[string]any{"addons": []any{dep, a}}
	})
	f.handle("GET /products/kubernetes/{id}/addons", func(r *fakeRequest) (int, any) {
		list := []any{}
		for _, a := range addons {
			if a["status"] == "installing" || a["status"] == "upgrading" {
				a["status"] = "installed"
			}
			list = append(list, a)
		}
		return http.StatusOK, map[string]any{"addons": list}
	})
	f.handle("POST /products/kubernetes/{id}/addons/{a}/update", func(r *fakeRequest) (int, any) {
		a := addons[r.Params["a"]]
		merge(a, r.Body, "version", "values")
		a["status"] = "upgrading"
		return http.StatusAccepted, map[string]any{"addon": a}
	})
	f.handle("POST /products/kubernetes/{id}/addons/{a}/uninstall", func(r *fakeRequest) (int, any) {
		if r.Body["confirm_data_loss"] != true {
			return http.StatusUnprocessableEntity, map[string]any{"success": false, "error": map[string]any{"code": "confirmation_required", "message": "confirm_data_loss fehlt"}}
		}
		delete(addons, r.Params["a"])
		return http.StatusAccepted, map[string]any{"addon": nil}
	})

	config := func(version string, pools string, values string) string {
		return providerConfig(f) + fmt.Sprintf(`
resource "reselling_kubernetes_cluster" "main" {
  name                  = "main"
  location_id           = "loc-1"
  cluster_id            = "infra-1"
  version_id            = %q
  control_plane_plan_id = "cp-plan"
  %s
}

resource "reselling_kubernetes_addon" "cert_manager" {
  cluster_id        = reselling_kubernetes_cluster.main.id
  addon             = "cert-manager"
  values_json       = jsonencode(%s)
  confirm_data_loss = true
}

data "reselling_kubernetes_kubeconfig" "main" {
  cluster_id = reselling_kubernetes_cluster.main.id
}`, version, pools, values)
	}
	twoPools := `
  worker_pool {
    name                      = "workers"
    plan_id                   = "w-plan"
    infrastructure_cluster_id = "infra-1"
    min_nodes                 = 2
    max_nodes                 = 4
  }
  worker_pool {
    name                      = "batch"
    plan_id                   = "w-plan"
    infrastructure_cluster_id = "infra-1"
    min_nodes                 = 1
    max_nodes                 = 2
  }`
	changedPools := `
  worker_pool {
    name                      = "workers"
    plan_id                   = "w-plan"
    infrastructure_cluster_id = "infra-1"
    min_nodes                 = 3
    max_nodes                 = 6
    auto_scale                = true
  }
  worker_pool {
    name                      = "gpu"
    plan_id                   = "gpu-plan"
    infrastructure_cluster_id = "infra-2"
    min_nodes                 = 1
    max_nodes                 = 1
  }`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("v-131", twoPools, `{ installCRDs = true }`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("reselling_kubernetes_cluster.main", "status", "running"),
					resource.TestCheckResourceAttr("reselling_kubernetes_cluster.main", "worker_pool.#", "2"),
					resource.TestCheckResourceAttr("reselling_kubernetes_cluster.main", "worker_pool.1.name", "batch"),
					resource.TestCheckResourceAttrSet("reselling_kubernetes_cluster.main", "worker_pool.0.id"),
					resource.TestCheckResourceAttr("reselling_kubernetes_cluster.main", "control_plane_count", "1"),
					resource.TestCheckResourceAttr("reselling_kubernetes_addon.cert_manager", "status", "installed"),
					resource.TestCheckResourceAttr("data.reselling_kubernetes_kubeconfig.main", "kubeconfig", "apiVersion: v1\nkind: Config\n"),
				),
			},
			{
				Config: config("v-132", changedPools, `{ installCRDs = false }`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("reselling_kubernetes_cluster.main", "version_id", "v-132"),
					resource.TestCheckResourceAttr("reselling_kubernetes_cluster.main", "worker_pool.#", "2"),
					resource.TestCheckResourceAttr("reselling_kubernetes_cluster.main", "worker_pool.0.min_nodes", "3"),
					resource.TestCheckResourceAttr("reselling_kubernetes_cluster.main", "worker_pool.1.name", "gpu"),
					resource.TestCheckResourceAttr("reselling_kubernetes_addon.cert_manager", "values_json", `{"installCRDs":false}`),
					func(*terraform.State) error {
						if upgrades != 1 {
							return fmt.Errorf("expected one upgrade, got %d", upgrades)
						}
						return nil
					},
				),
			},
			{ResourceName: "reselling_kubernetes_cluster.main", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"}},
			{
				ResourceName: "reselling_kubernetes_addon.cert_manager", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc:       childImportID("reselling_kubernetes_addon.cert_manager", "cluster_id"),
				ImportStateVerifyIgnore: []string{"timeouts", "confirm_data_loss"},
			},
		},
	})
	if len(addons) != 0 {
		t.Fatal("add-on not removed")
	}
	for _, c := range clusters {
		if c["status"] != "pending_deletion" {
			t.Fatal("cluster not deleted")
		}
	}
}

func TestImageAndRecipe(t *testing.T) {
	f := newFakeAPI(t)
	images := map[string]map[string]any{}
	recipes := map[string]map[string]any{}
	f.handle("POST /products/rootserver/images", func(r *fakeRequest) (int, any) {
		img := map[string]any{
			"id": f.id(), "name": r.Body["name"], "description": r.Body["description"], "status": "creating", "labels": r.Body["labels"],
			"os": map[string]any{"id": "os-debian-12"}, "cluster": map[string]any{"id": "cluster-1"}, "location": map[string]any{"id": "loc-1"},
			"size_gb": nil, "min_disk_gb": 20, "generalized": r.Body["generalize"], "shared_with_sub_resellers": false, "owned": true,
			"source_server": map[string]any{"id": r.Body["server_id"]},
		}
		images[img["id"].(string)] = img
		return http.StatusAccepted, map[string]any{"image": img, "task_id": "t1"}
	})
	f.handle("GET /products/rootserver/images/{id}", func(r *fakeRequest) (int, any) {
		img, ok := images[r.Params["id"]]
		if !ok {
			return notFound()
		}
		if img["status"] == "creating" {
			img["status"] = "available"
			img["size_gb"] = 4
		}
		return http.StatusOK, map[string]any{"image": img}
	})
	f.handle("PATCH /products/rootserver/images/{id}", func(r *fakeRequest) (int, any) {
		merge(images[r.Params["id"]], r.Body, "name", "description", "labels", "shared_with_sub_resellers")
		return http.StatusOK, map[string]any{"image": images[r.Params["id"]]}
	})
	f.handle("DELETE /products/rootserver/images/{id}", func(r *fakeRequest) (int, any) {
		delete(images, r.Params["id"])
		return http.StatusAccepted, map[string]any{"deleted": true}
	})
	f.handle("POST /products/rootserver/images/recipes", func(r *fakeRequest) (int, any) {
		rc := copyMap(r.Body)
		rc["id"] = f.id()
		recipes[rc["id"].(string)] = rc
		return http.StatusCreated, map[string]any{"recipe": rc}
	})
	f.handle("GET /products/rootserver/images/recipes/{id}", func(r *fakeRequest) (int, any) {
		rc, ok := recipes[r.Params["id"]]
		if !ok {
			return notFound()
		}
		return http.StatusOK, map[string]any{"recipe": rc}
	})
	f.handle("PUT /products/rootserver/images/recipes/{id}", func(r *fakeRequest) (int, any) {
		rc := copyMap(r.Body)
		rc["id"] = r.Params["id"]
		recipes[r.Params["id"]] = rc
		return http.StatusOK, map[string]any{"recipe": rc}
	})
	f.handle("DELETE /products/rootserver/images/recipes/{id}", func(r *fakeRequest) (int, any) {
		delete(recipes, r.Params["id"])
		return http.StatusOK, map[string]any{"deleted": true}
	})
	config := func(name string, cores int) string {
		return providerConfig(f) + fmt.Sprintf(`
resource "reselling_rootserver_image" "golden" {
  server_id  = "server-1"
  name       = %q
  labels     = ["golden"]
  generalize = true
}

resource "reselling_image_recipe" "base" {
  name          = "base"
  os_version_id = "os-debian-12"
  cores         = %d
  ram           = 2048
  disk          = 20

  step {
    type              = "startup_script"
    startup_script_id = "script-1"
  }
  step {
    type    = "user_data"
    content = "#cloud-config\npackages: [htop]\n"
  }
}`, name, cores)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("golden", 2),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("reselling_rootserver_image.golden", "status", "available"),
					resource.TestCheckResourceAttr("reselling_rootserver_image.golden", "size_gb", "4"),
					resource.TestCheckResourceAttr("reselling_image_recipe.base", "step.#", "2"),
				),
			},
			{
				Config: config("golden-v2", 4),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("reselling_rootserver_image.golden", "name", "golden-v2"),
					resource.TestCheckResourceAttr("reselling_image_recipe.base", "cores", "4"),
				),
			},
			{ResourceName: "reselling_rootserver_image.golden", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts", "stop_server"}},
			{ResourceName: "reselling_image_recipe.base", ImportState: true, ImportStateVerify: true},
		},
	})
	if len(images)+len(recipes) != 0 {
		t.Fatal("image or recipe not deleted")
	}
}
