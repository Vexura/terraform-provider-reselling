data "reselling_locations" "fra" {
  name = "Frankfurt"
}

output "clusters" {
  value = data.reselling_locations.fra.locations[0].clusters[*].name
}
