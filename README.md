# terraform-provider-pyxcloud

Terraform provider per [PyxCloud](https://passo.build). Su [`terraform-plugin-framework`](https://github.com/hashicorp/terraform-plugin-framework).

## Cosa fa

Prende una **topologia canonica** (modello provider-indipendente: VM, DB, LB, object-storage, rete, security-group) e la traduce in risorse concrete AWS, GCP, DigitalOcean, OVH. Usa lo **stesso catalogo censito** del wizard e della Compare page — niente mappe hard-coded di regioni o instance type. Se un componente non esiste sul provider target, errore a plan time.

## Risorse Terraform

| Risorsa / Data source | Descrizione |
|---|---|
| `pyxcloud_topology` | CRUD di topologia canonica. Componenti: `network`, `security_group`, `virtual_machine`, `virtual_machine_scale_group`, `load_balancer`, `managed_database`, `object_storage`, `web_service`, `pipeline_control_plane` |
| `pyxcloud_compare` | Data source. Prezza una topologia su coppie (provider, regione), ritorna cheapest-first |

## CLI (4)

| Comando | Cosa fa |
|---|---|
| `pyxnet-render` | Traduce `network_plan` strutturato in HCL provider-specifico (VPC + subnet). Usato nei round-trip test |
| `pyxcost-scan` | Scansiona costi billing contro soglie (budget-overrun, cost-spike, anomaly). Blocca deploy se sfora |
| `pyxenv-render` | Render deploy di ambiente da topologia canonica |
| `pyxsec-scan` | Scansione IaC security: hardcoded secret, bucket pubblici, 0.0.0.0/0 su porte sensibili, KMS default, wildcard IAM. Blocca deploy se viola |

## Catalogo

Stesso DB censito usato da wizard e Compare page. Tabelle: `region`, `virtual_machine`, `virtual_machine_price`, `managed_database`, `managed_database_price`, `load_balancer`, `load_balancer_price`, `blob_storage`, `blob_storage_price`. Provider chiama BE (`/api/topology`, `/api/compare`, `/api/translate`) — mai embedding di mappe provider.

## Documentazione

- [SPEC.md](SPEC.md) — specifica completa, componenti, contratti di traduzione, operator pattern, roadmap wave-1/wave-2
- [MIGRATION.md](MIGRATION.md) — motore di migrazione provider-to-provider (CRIU + rsync, cutover, rollback)
- [DEPLOY-GATE.md](DEPLOY-GATE.md) — deploy gate: billing signal scan, IaC security scan
- `docs/` — dettagli implementativi per componente

## Build

```sh
go mod tidy
go build ./...
go vet ./...
go test ./...
```

## Licenza

MPL 2.0. Vedi [LICENSE](LICENSE).
