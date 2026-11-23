resource "reselling_vpc" "backend" {
  name        = "backend"
  subnet      = "10.20.0.0/24"
  location_id = data.reselling_locations.fra.locations[0].id
}
