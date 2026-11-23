data "reselling_storage_classes" "fra" {
  cluster_id = data.reselling_locations.fra.locations[0].clusters[0].id
}
