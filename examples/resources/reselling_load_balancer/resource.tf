data "reselling_load_balancer_plans" "small" {
  name = "small"
}

resource "reselling_load_balancer" "web" {
  name        = "web-lb"
  location_id = reselling_vpc.backend.location_id
  vpc_id      = reselling_vpc.backend.id
  plan_id     = data.reselling_load_balancer_plans.small.plans[0].id
}
