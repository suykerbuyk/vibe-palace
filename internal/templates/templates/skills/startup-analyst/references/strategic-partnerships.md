# Strategic Partnership Landscape — Reference

## Partnership Framework

Not all partnerships are equal. Before recommending a partnership, classify it:

| Type | Description | What You Get | What You Give | Risk |
|---|---|---|---|---|
| **Distribution** | Partner sells your product to their customer base | Access to established relationships | Revenue share, co-marketing commitment | Dependency on partner motivation |
| **Data** | Partner licenses data you need to build the product | Proprietary data moat | Revenue or equity | Lock-in, pricing power reversal |
| **Technology** | Partner provides complementary infrastructure | Reduced build cost, credibility | Integration commitment, joint GTM | Platform dependency |
| **Credibility** | Partner endorses or co-develops with you | Trust transfer in new market | IP disclosure risk, timeline alignment | Partner reputation risk |
| **White-label / OEM** | Incumbent embeds your capability | Revenue at scale, fast distribution | IP exposure, pricing pressure | Commoditization of your feature |
| **Co-development** | Joint build of shared capability | Shared cost, shared IP | Loss of exclusivity, governance complexity | Misaligned incentives |

---

## Identifying Partnership Candidates

### Step 1: Map the Value Chain
For any business plan, list every actor in the value chain:
- Who creates the underlying data/content?
- Who aggregates or structures it today?
- Who distributes it to the end buyer?
- Who advises the buyer on how to use it?
- Who enforces compliance with it?

Each actor is a potential partner or a potential competitor.

### Step 2: Identify Mutual Value Triggers
A partnership only materializes when both sides have a concrete reason to engage now:
- **Partner has a customer asking for something they can't build:** Your product solves their feature gap
- **Partner has data you need and you have distribution they lack**
- **Partner has credibility you need and you have technology they lack**
- **Regulatory mandate creates joint urgency** (e.g., Pillar Two forcing function creates demand for treaty graph at Big 4)

### Step 3: Assess Partnership Leverage
What do you offer that they cannot easily replicate internally?
- If the answer is "not much" — the partnership becomes an acquisition discussion
- If the answer is "novel IP and first-mover advantage" — the partnership is on your terms

---

## Partnership Categories for Regulatory Knowledge / RegTech

### Data Partnerships

**Legal / Regulatory Content Providers:**
- **EUR-Lex / Publications Office of the EU:** Official EU law; freely available but parsing/normalization is the hard part. Partnership value: structured API access, advance notice of new publications.
- **eCFR / Federal Register (US GPO):** Similar — free, but normalized data requires work. Partnership value: structured bulk export agreements.
- **LexisNexis / RELX:** Premium legal content; licensing is expensive but they have everything. Partnership risk: they may see you as a competitor or acquisition target.
- **Thomson Reuters / Westlaw:** Same dynamic as LexisNexis. Data licensing plus potential OEM interest.
- **Wolters Kluwer:** Deep regulatory content library; partnership or white-label opportunity exists.

**Treaty / Tax Data:**
- **IBFD (International Bureau of Fiscal Documentation):** The definitive source for bilateral treaty text and country tax guides. Licensing is the path; partnership is possible if the product enhances their data's utility.
- **Bloomberg Tax / Tax Analysts:** Tax treaty database; licensing discussion is the entry point.

### Technology Partnerships

**Graph Infrastructure:**
- **Neo4j:** Already has regulatory dependency mapping case studies. A reference partnership (they showcase your use case; you use their enterprise product) is achievable and mutually beneficial. Entry point: developer relations team.
- **AWS / Azure / GCP:** All have regulatory compliance programs that provide co-marketing, credits, and marketplace listing. AWS Marketplace listing alone provides access to thousands of enterprise procurement relationships.
- **Ontotext / Graphwise.ai:** Ontology-driven regulatory mapping; possible white-label or data layer partnership rather than competition.

**AI / LLM:**
- **Anthropic:** Claude API for regulatory text interpretation and normalization. Anthropic has an enterprise compliance partnership program.
- **Microsoft Azure OpenAI:** Strongly positioned for regulated industries due to data residency and compliance certifications.
- **Cohere:** Strong enterprise focus, compliance-aware LLM deployment.

### Distribution Partnerships

**Big 4 Consulting Firms:**
- PwC, Deloitte, EY, KPMG all have in-house RegTech practices and client relationships in every target vertical.
- Partnership model: They recommend / resell the platform as part of compliance engagements. Revenue share: typically 20–30% of ACV.
- Entry point: RegTech/innovation leads within each firm's FS or tax practice.
- Reality check: Big 4 partnerships take 12–18 months to structure and rarely produce revenue in year 1.

**Legal Publishers:**
- Wolters Kluwer, LexisNexis, Thomson Reuters all have distribution to law firms and compliance departments.
- Partnership model: White-label embedding within existing research platform.
- Risk: They are also potential acquirers; partnership discussions often transition into acquisition conversations.

**Trade Associations:**
- **SIFMA** (securities industry): Distribution to financial services compliance departments
- **ISDA:** Derivatives-focused; relevant for contract network vertical
- **ACAMS:** Anti-money laundering compliance professional network
- **IIA (Institute of Internal Auditors):** Internal audit and GRC community

**Cloud Marketplaces:**
- AWS Marketplace, Azure Marketplace, Google Cloud Marketplace
- Enterprise buyers increasingly procure SaaS through cloud marketplaces (uses committed cloud spend)
- Listing is achievable in 60–90 days; meaningful revenue requires investment in marketplace GTM

### Credibility / Validation Partnerships

**Academic Institutions:**
- University law schools with computational law programs (Stanford CodeX, MIT Computational Law, Harvard BLP)
- Validates novel concepts (three-axis coordinate system, etc.)
- Can provide peer review and publication pathway
- STTR grant pathway (requires formal university partnership)

**Standards Bodies:**
- **Akoma Ntoso / LegalDocML (OASIS):** The existing open standard for legislative XML. Partnership or contribution here gives schema legitimacy.
- **OMG (Object Management Group):** Standards for regulatory and governance ontologies
- **OECD BEPS Inclusive Framework:** Relevant for tax pathway vertical; being recognized by OECD as a tool for Pillar Two compliance would be transformative

**Regulatory Authorities:**
- Central bank innovation hubs (Bank of England, ECB, Federal Reserve FinTech programs)
- CFTC LabCFTC, SEC EDGAR / FinHUB
- Partnership model: Regulator uses the platform for SupTech (supervisory tech); provides regulatory legitimacy
- This is a long-game play but a high-value one

---

## Partnership Prioritization Template

Use this to rank partnership opportunities:

| Partner | Type | Mutual Value Clarity | Effort to Initiate | Time to Revenue | Priority |
|---|---|---|---|---|---|
| Neo4j | Technology/Credibility | High | Low (developer relations) | 0 (usage, not revenue) | High |
| AWS Marketplace | Distribution | High | Medium (60–90 days) | 12–18 months | High |
| IBFD | Data | High | Medium (licensing negotiation) | Immediate (cost reduction) | High |
| Big 4 (EY or Deloitte) | Distribution | Medium | High (12–18 months) | 18–24 months | Medium |
| Stanford CodeX | Credibility/STTR | High | Low (academic engagement) | 6–12 months (grant) | High |
| OASIS/Akoma Ntoso | Standards credibility | Medium | Low (contribute to standard) | Long-term | Medium |
| Thomson Reuters | Data/OEM | High | High (legal/commercial) | Uncertain | Low (acquire risk) |

---

## Partnership Anti-Patterns

1. **Announcing partnerships before they produce revenue:** Creates expectation without substance. Name partnerships only when there is a signed agreement or revenue flowing.
2. **Partnerships that require the partner to change their behavior:** The best partnerships fit into what the partner already does, not what you wish they did.
3. **Treating a Big 4 as a sales channel before product-market fit:** They will test the product with a client; if it fails, the relationship ends.
4. **Underestimating partnership legal overhead:** A data licensing agreement with a major legal publisher will take 6–9 months and cost $20K–$50K in legal fees.
5. **Ignoring the "partner as acquirer" dynamic:** Many partnership discussions in RegTech end in acquisition conversations. Know your walk-away price before you share your architecture.
