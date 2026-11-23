data "reselling_database_plans" "small" {
  name = "small"
}

resource "reselling_database" "shop" {
  name            = "shop-db"
  engine          = "postgresql"
  version         = "17"
  location_id     = reselling_vpc.backend.location_id
  vpc_id          = reselling_vpc.backend.id
  plan_id         = data.reselling_database_plans.small.plans[0].id
  trusted_sources = ["10.20.0.0/26"]

  public_access  = true
  public_sources = ["203.0.113.10/32"]

  maintenance_day  = 7 # Sunday
  maintenance_hour = 3
}
