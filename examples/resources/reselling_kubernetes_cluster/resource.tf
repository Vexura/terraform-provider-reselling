data "reselling_kubernetes_versions" "all" {}
data "reselling_kubernetes_plans" "all" {}

resource "reselling_kubernetes_cluster" "main" {
  name                  = "main"
  location_id           = data.reselling_locations.fra.locations[0].id
  cluster_id            = data.reselling_locations.fra.locations[0].clusters[0].id
  version_id            = data.reselling_kubernetes_versions.all.default_id
  control_plane_plan_id = [for p in data.reselling_kubernetes_plans.all.plans : p.id if p.is_control_plane][0]
  control_plane_count   = 3
  vpc_id                = reselling_vpc.backend.id

  worker_pool {
    name                      = "workers"
    plan_id                   = [for p in data.reselling_kubernetes_plans.all.plans : p.id if !p.is_control_plane][0]
    infrastructure_cluster_id = data.reselling_locations.fra.locations[0].clusters[0].id
    min_nodes                 = 2
    max_nodes                 = 5
    auto_scale                = true
  }
}
