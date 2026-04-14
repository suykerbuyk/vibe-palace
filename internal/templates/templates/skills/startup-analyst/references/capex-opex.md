# CapEx / OpEx Estimation Framework — Reference

## Principles

1. **Always present three scenarios:** Low (lean, everything goes right), Base (realistic), High (realistic with setbacks)
2. **Show your work:** Every estimate should have a named assumption. "Compute: $8K/month base (assumption: 10TB active graph on AWS, ~50K daily API calls)"
3. **Separate one-time from recurring:** CapEx is not the same as "things you pay once." Use CapEx for assets that depreciate; OpEx for ongoing operational cost.
4. **Don't hide headcount:** Personnel is typically 60–75% of early-stage tech startup burn. Undercounting headcount is the most common modeling error.
5. **Regulatory and legal costs are always underestimated:** Compliance-adjacent products require ongoing legal review. Budget for it.

---

## CapEx Categories (One-Time or Multi-Year Assets)

### Technology Infrastructure Setup
- Cloud environment configuration and security hardening
- Graph database licensing (if commercial: Neo4j Enterprise, TigerGraph, Amazon Neptune)
- Data ingestion pipeline construction
- Initial data acquisition, cleaning, and normalization
- Dev/staging/prod environment setup
- Security audit and penetration testing (SOC 2, ISO 27001 prep)

**Typical range for a B2B SaaS knowledge graph platform:**
- Lean (open-source stack, small team): $150K–$400K
- Base (mixed commercial/open-source, 4–6 engineers): $500K–$1.2M
- Heavy (enterprise-grade from day one): $1.5M–$4M+

### Data Acquisition (Often CapEx-Like Even if Technically OpEx)
Regulatory text normalization is a substantial capital item for regulatory knowledge platforms:
- Parsing and ingesting eCFR, EUR-Lex, national regulatory APIs: $50K–$200K (engineering time)
- Bilateral tax treaty corpus (150+ treaties): $20K–$80K (legal review and structuring)
- Proprietary data licensing (if needed): $50K–$500K/year (becomes OpEx)
- Initial graph population (manual curation + AI-assisted): $100K–$500K

### Legal and Compliance Setup
- Corporate structure and IP assignment: $10K–$30K
- Terms of service and liability framework (especially for legal/regulatory content): $20K–$60K
- Privacy policy and data handling review (GDPR, CCPA): $15K–$40K
- Outside counsel opinion on "practice of law" risk: $15K–$25K

---

## OpEx Categories (Monthly Recurring)

### Compute and Infrastructure (Monthly)

For a knowledge graph platform with temporal versioning and cascade alerting:

| Component | Low | Base | High |
|---|---|---|---|
| Graph database hosting (managed or self-hosted) | $500 | $2,500 | $8,000 |
| Object storage (regulatory snapshots, hash chains) | $100 | $400 | $1,500 |
| Compute (API servers, ingestion workers) | $800 | $3,000 | $10,000 |
| Search / vector index (Elasticsearch, Pinecone, etc.) | $300 | $1,200 | $4,000 |
| LLM API costs (if AI-assisted normalization or querying) | $500 | $2,000 | $8,000 |
| CDN, networking, egress | $100 | $400 | $1,500 |
| Monitoring, logging, security tooling | $200 | $800 | $2,500 |
| **Monthly compute total** | **~$2.5K** | **~$10.3K** | **~$35.5K** |

### Headcount (Monthly Fully-Loaded, US Market)

| Role | FTE | Monthly (Low) | Monthly (Base) | Monthly (High) |
|---|---|---|---|---|
| Founding engineer / CTO | 1 | $12,000 | $16,000 | $22,000 |
| Backend engineer (graph/data) | 1–2 | $10,000 | $14,000 | $20,000 |
| Frontend/product engineer | 1 | $9,000 | $12,000 | $18,000 |
| Domain expert (regulatory/legal) | 0.5–1 | $8,000 | $12,000 | $18,000 |
| Sales / BD (post-MVP) | 1 | $8,000 | $12,000 | $18,000 |
| CEO / business lead | 1 | $8,000 | $14,000 | $20,000 |

**Total monthly headcount (5–6 person team, base):** ~$80K–$100K/month

### Data and Licensing (Monthly)
- Regulatory data feed subscriptions (if any): $500–$5,000
- LegalTech API licenses (WK, TR if using any enrichment): $1,000–$10,000
- Developer tools and SaaS (GitHub, CI/CD, project management): $500–$2,000

### Legal and Compliance (Monthly)
- Outside counsel retainer (compliance-adjacent product): $2,000–$8,000
- D&O insurance prep (pre-institutional round): $500–$2,000
- Ongoing privacy/security compliance: $500–$2,000

---

## Burn Rate Summary (Monthly Cash Consumption)

| Stage | Monthly Burn | Key Assumption |
|---|---|---|
| MVP development (pre-launch, 3–4 people) | $45K–$70K | Small founding team, lean stack |
| Post-MVP, customer development (5–6 people) | $80K–$120K | First BD hire, infra scaling |
| Early commercial (8–10 people, first customers) | $150K–$220K | Sales, support, legal overhead |
| Growth stage (15+ people, Series A burn) | $300K–$500K+ | Full GTM, data ops, enterprise support |

---

## Unit Economics Framework

### Cost Per Customer (at Scale)
- Customer success / support hours per account per month
- Compute allocated per customer (based on query volume and data scope)
- Data refresh cost per customer jurisdiction scope
- Legal/compliance overhead per customer contract

### Minimum Viable ACV
To achieve 40% gross margin (typical B2B SaaS floor for investor viability):
- If cost to serve = $24K/year (base scenario, 10-customer cohort): minimum ACV ≈ $40K
- If cost to serve = $60K/year (enterprise, broad jurisdiction coverage): minimum ACV ≈ $100K

### Payback Period Check
- Enterprise sales cycle: 12–24 months
- Implementation + onboarding: 2–4 months
- Net payback on CAC: Should be < 24 months for SaaS viability
- If sales cycle = 18 months and ACV = $60K, customer acquisition cost ceiling ≈ $40K–$60K

---

## Common Estimation Errors to Flag

1. **Missing legal/compliance line item** — Especially dangerous for RegTech; budget $5K–$15K/month minimum
2. **Single-point compute estimate** — Query volume and data scope variance is high; always show range
3. **Underestimated data ops cost** — Regulatory text is messy; 3–5x more expensive to normalize than to ingest
4. **No customer success budget** — Enterprise customers require hand-holding; this is a real cost
5. **Missing security certification budget** — SOC 2 Type II costs $30K–$80K in year one; required for enterprise
6. **Founder salaries at zero** — Unsustainable; models should include market-rate salaries for planning purposes
