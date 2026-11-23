variable "location" {
  description = "Part of the location name, for example Frankfurt."
  type        = string
  default     = "Frankfurt"
}

variable "domain" {
  description = "Domain of the shop; it must point to the load balancer for the Let's Encrypt certificate."
  type        = string
  default     = "shop.example.com"
}

variable "ssh_public_key" {
  description = "Public SSH key for root."
  type        = string
}

variable "admin_cidr" {
  description = "Network that may connect with SSH."
  type        = string
  default     = "198.51.100.0/24"
}

variable "db_password_generation" {
  description = "Change this value to rotate the database password."
  type        = string
  default     = "1"
}
