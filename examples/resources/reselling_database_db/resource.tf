resource "reselling_database_db" "shop" {
  database_id = reselling_database.shop.id
  name        = "shop"
}
