data "reselling_kubernetes_kubeconfig" "main" {
  cluster_id = reselling_kubernetes_cluster.main.id
}

resource "local_sensitive_file" "kubeconfig" {
  content  = data.reselling_kubernetes_kubeconfig.main.kubeconfig
  filename = "${path.module}/kubeconfig.yaml"
}
