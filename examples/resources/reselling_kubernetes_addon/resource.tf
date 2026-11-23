resource "reselling_kubernetes_addon" "cert_manager" {
  cluster_id  = reselling_kubernetes_cluster.main.id
  addon       = "cert-manager"
  values_json = jsonencode({ installCRDs = true })
}
