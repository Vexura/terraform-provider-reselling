output "load_balancer_ipv4" {
  description = "Point the domain here."
  value       = reselling_load_balancer.web.ipv4_address
}

output "servers" {
  value = { for s in reselling_rootserver.web : s.hostname => s.primary_ip }
}

output "volume_path" {
  description = "Device path of the data volume inside web1."
  value       = reselling_rootserver_volume_attachment.data.guest_device_path
}

output "database_host" {
  value = reselling_database.app.host
}

output "database_url" {
  value     = "postgresql://${reselling_database_user.app.username}:${reselling_database_user.app.password}@${reselling_database.app.host}:${reselling_database.app.port}/${reselling_database_db.app.name}?sslmode=require"
  sensitive = true
}
