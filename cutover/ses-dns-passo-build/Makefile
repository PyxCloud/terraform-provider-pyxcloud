# SES DNS restore — passo.build. One-shot, review-gated, bounded-substitution root.
#
# Targets are ordered and fail-closed: nothing in this Makefile ever touches a
# record outside the 3 DKIM names + _amazonses.passo.build. All Cloudflare calls
# are GET (read-only) except the single terraform apply, and the apply itself is
# bounded by make plan-check (exact action counts).

ZONE    := 155b6fa434a24bdaba68a60ba87ea134
SNAPDIR := snapshots

.PHONY: re-measure snapshot list-records init import plan plan-check apply verify postverify-destroy validate

## validate: offline schema validation (no token, no backend).
validate:
	cd $(CURDIR) && terraform init -backend=false -input=false >/dev/null && terraform validate

## re-measure: pull the CURRENT tokens/verification token from SES (mandatory).
re-measure:
	@aws sesv2 get-email-identity --region eu-west-1 --email-identity passo.build \
	  --query '{status:DkimAttributes.Status,tokens:DkimAttributes.Tokens}' --output json
	@aws ses get-identity-verification-attributes --region eu-west-1 --identities passo.build \
	  --query 'VerificationAttributes."passo.build".VerificationToken' --output text

## snapshot: BEFORE-snapshot of the exact live record-set we are authorized to
## touch (read-only GET; record ids/types/names/contents captured for rollback).
## This file is the immutable rollback source of truth.
snapshot:
	@mkdir -p $(SNAPDIR)
	@STAMP=$$(date -u +%Y%m%dT%H%M%SZ); \
	CLOUDFLARE_ZONE=$$ZONE $(CURDIR)/snapshot.sh $(SNAPDIR)/dns-before-$$STAMP.json; \
	echo "snapshot: $(SNAPDIR)/dns-before-$$STAMP.json"

## list-records: same read-only listing, to stdout, for the human review gate.
list-records:
	@CLOUDFLARE_ZONE=$$ZONE $(CURDIR)/snapshot.sh /dev/stdout

## init: provider only, no backend.
init:
	cd $(CURDIR) && terraform init -backend=false -input=false

## import: adopt the 3 live placeholder TXT records into the ses_dkim addresses
## (imports are skipped cleanly for labels that have no live record).
import:
	@CLOUDFLARE_ZONE=$(ZONE) $(CURDIR)/import-guard.sh

## plan: read-only plan, saved for the review gate.
plan:
	cd $(CURDIR) && terraform plan -input=false -out=tfplan

## plan-check: the apply veto gate. Plan must show EXACTLY
## 3 destroy + 4 create + 0 change; ANY other action, destroy or update is a veto.
plan-check:
	cd $(CURDIR) && test -f tfplan || { echo "run make plan first"; exit 1; }
	cd $(CURDIR) && terraform show -json tfplan > tfplan.json
	cd $(CURDIR) && ./plan-guard.sh tfplan.json

## apply: the single write. Root review must be approved BEFORE this target.
apply:
	cd $(CURDIR) && terraform apply -input=false tfplan

## verify: SES identity status (read-only). Allow up to 72h, typically <10 min.
verify:
	@aws sesv2 get-email-identity --region eu-west-1 --email-identity passo.build \
	  --query '{VerificationStatus:VerificationStatus,DkimStatus:DkimAttributes.Status}' --output json

## postverify-destroy: once verify shows SUCCESS, empty the local state on
## purpose so this one-shot root can never drift-fight the canonical
## DNS-owning pipeline. Refuses to run before SUCCESS.
postverify-destroy:
	@aws sesv2 get-email-identity --region eu-west-1 --email-identity passo.build \
	  --query 'VerificationStatus' --output text | grep -qx SUCCESS \
	  || { echo "VETO: identity not SUCCESS yet; leave state in place"; exit 1; }
	cd $(CURDIR) && terraform destroy -input=false && rm -f tfplan tfplan.json
	@echo "state emptied on purpose; records stay live, owned by the canonical pipeline"
