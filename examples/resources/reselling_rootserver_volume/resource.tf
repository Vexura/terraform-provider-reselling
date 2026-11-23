data "reselling_storage_classes" "fra" {
  cluster_id = data.reselling_locations.fra.locations[0].clusters[0].id
}

resource "reselling_rootserver_volume" "data" {
  name             = "data"
  storage_class_id = data.reselling_storage_classes.fra.storage_classes[0].id
  size_gb          = 100
}
