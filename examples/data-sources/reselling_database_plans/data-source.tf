data "reselling_database_plans" "all" {}

output "engines" {
  value = data.reselling_database_plans.all.engines
}
