terraform {
  required_version = ">= 1.11"
  required_providers {
    reselling = {
      source  = "vexura/reselling"
      version = "~> 0.1"
    }
  }
}

# The API key is read from RESELLING_API_KEY when api_key is not set.
provider "reselling" {
  # endpoint = "https://api.resellingprovider.de/api/v1" (default)
  # api_key  = var.reselling_api_key
  timeout = "60s"
}
