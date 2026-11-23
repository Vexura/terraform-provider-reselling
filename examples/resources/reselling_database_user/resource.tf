resource "reselling_database_user" "app" {
  database_id = reselling_database.shop.id
  username    = "shop_app"
  rotate      = "2026-11" # change to rotate the password

  grants = [{
    db_id     = reselling_database_db.shop.id
    privilege = "owner"
  }]
}

output "db_password" {
  value     = reselling_database_user.app.password
  sensitive = true
}
