# terraform-provider-portallocator

Custom Terraform provider that allocates TCP ports to systemd units.

## What it does

- Allocates a free port in `[20000, 29999]` keyed by a service name, persisting
  the choice to `/etc/port_allocator/allocations.json`.
- Writes a systemd drop-in at `/etc/systemd/system/<service>.service.d/override.conf`
  containing `Environment=PORT=<n>`.
- Does not run `systemctl daemon-reload` or restart the unit; that's the .deb
  postinst's job.

## Resource

```hcl
resource "portallocator_allocation" "hello" {
  service = "hello-app"
}
```

Outputs:

- `port` — the allocated port.

## Consume from GitHub

```hcl
terraform {
  required_providers {
    portallocator = {
      source  = "github.com/rsxdalv/terraform-provider-portallocator"
      version = ">= 1.0.0"
    }
  }
}
```
