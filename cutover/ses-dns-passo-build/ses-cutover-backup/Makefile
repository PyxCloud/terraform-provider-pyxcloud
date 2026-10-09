# cutover/Makefile — persistent DO cutover deploy harness (pd-MIG-CUTOVER-F2-02).
#
# The canonical way to apply the AWS->DO cutover baseline. Reproducible: renders
# committed catalog HCL into cutover/generated/ and applies it against the
# persistent S3 state. See README.md for the credential exports (from Secrets
# Manager) these targets assume are already in the environment.

GEN      := generated
SSH_KEYS ?= ["57496891"]
TARGET   ?=

.PHONY: render init plan apply plan-mcp apply-mcp validate clean

## render: regenerate generated/*.tf from the committed catalog descriptor.
render:
	cd .. && go run ./cutover/render.go

## init: terraform init against the persistent S3 backend.
init: render
	cd $(GEN) && terraform init -input=false

## validate: offline schema validation (no backend, no creds).
validate: render
	cd $(GEN) && terraform init -backend=false -input=false >/dev/null && terraform validate

## plan: full-estate plan (expect 0 to destroy).
plan: init
	cd $(GEN) && terraform plan -input=false -var 'do_ssh_keys=$(SSH_KEYS)'

## apply: full-estate apply.
apply: init
	cd $(GEN) && terraform apply -input=false -var 'do_ssh_keys=$(SSH_KEYS)'

## plan-mcp: targeted plan of the durable mcp scale-group only.
plan-mcp: init
	cd $(GEN) && terraform plan -input=false -target=digitalocean_droplet_autoscale.mcp -var 'do_ssh_keys=$(SSH_KEYS)'

## apply-mcp: targeted apply of the durable mcp scale-group only.
apply-mcp: init
	cd $(GEN) && terraform apply -input=false -target=digitalocean_droplet_autoscale.mcp -var 'do_ssh_keys=$(SSH_KEYS)'

## clean: remove the rendered (secret-bearing) output and terraform working dir.
clean:
	rm -rf $(GEN)
