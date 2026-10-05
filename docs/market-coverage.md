# Market Coverage — dataset pinato (2026-10-05, ±1pt)

Fonte: censimento stimato share IaaS/PaaS worldwide (stessa base del
roadmap v2 §2, no claim singole market-share report linkabili: numeri di
censimento, non benchmark). Da NON usare in marketing senza ratifica dei link.

Regole di conteggio:

1. **akamai/linode contano UNA volta** (stessa azienda dal 2022): la lista
   provider tiene entrambe le voci per copertura render, la quota no.
2. **vsphere è private cloud, escluso dal denominatore** IaaS pubblico
   (supportato come provider ma fuori quota).
3. **ubicloud quota ~0** (startup, incluso per il deploy path).
4. Ogni cella della matrice component×provider deve essere `N` o `M`
   (0 `none`) — verificato da `pyxcoverage-matrix`.

## Quota attuale (verdetto del tool)

```
censited+supported: 84.0% | gap esplicito: 12.2% | totale pinato: 96.2%
QUOTA CUMULATIVA ATTUALE: 87.3%
VERDETTO: 87.3% < 95% — il claim totale richiede gli adapter del gap sopra
```

## Provider censiti e supportati

| provider | quota % |
|---|---|
| aws | 30.0 |
| azure | 22.0 |
| gcp | 12.0 |
| alicloud | 4.0 |
| oracle | 3.0 |
| tencent | 2.5 |
| cloudflare | 2.5 |
| ibm | 1.5 |
| digitalocean | 1.5 |
| linode (akamai) | 1.0 |
| ovh | 1.0 |
| hetzner | 0.7 |
| rackspace | 0.7 |
| huawei | 0.7 |
| fastly | 0.3 |
| stackit | 0.2 |
| vultr | 0.2 |
| scaleway | 0.2 |
| ubicloud | ~0 |
| vsphere | fuori quota (private cloud) |
| **totale** | **84.0** |

## Gap residuo (richiede adapters FASE G / P6-bis)

| gap | quota % |
|---|---|
| tail aggregato (<0.3% ciascuno) | 4.5 |
| SAP Cloud (Hyperforce) | 1.5 |
| Salesforce (Hyperforce) | 1.5 |
| CoreWeave/Nebius-class GPU | 1.0 |
| Lumen/Flexential/Equinix-class colo | 1.0 |
| Baidu Cloud | 0.7 |
| UCloud/Zenlayer-class APAC | 0.7 |
| NTT Communications | 0.7 |
| JD Cloud | 0.6 |
| **totale gap** | **12.2** |

Totale pinato: 96.2%. Il target **≥95% sul totale del mercato è raggiungibile
SOLO dopo gli adapter del gap sopra** — il claim attuale onesto è 87.3%.

## Come rigenerare il verdetto

```
go run ./cmd/pyxcoverage-matrix   # sezione "market coverage" in fondo
```

Il dataset vive in `cmd/pyxcoverage-matrix/main.go` (`marketShare`,
`marketGap`): aggiorna QUEL file e QUESTO documento insieme.