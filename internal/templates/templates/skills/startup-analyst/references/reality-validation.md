# Reality Validation Framework — Reference

## Purpose
This framework surfaces internal contradictions, unsupported claims, dangerous assumptions,
and logical gaps in business plans before they surface in a funding conversation or — worse —
after capital has been deployed.

---

## Contradiction Detection Checklist

### Strategic Coherence
- [ ] Does the "core distinction" in Part 1 hold up against every competitor mentioned in the competitive section?
- [ ] Does the go-to-market strategy match the buyer persona's actual procurement process?
- [ ] If multiple verticals are claimed, is there a credible sequencing rationale, or are all verticals asserted simultaneously?
- [ ] Does the pricing model align with the stated buyer's budget authority and procurement cycle?
- [ ] Does the claimed "platform" abstraction require the first vertical to be successful before it's credible?

### Technical Claims
- [ ] Is "publicly available data" actually free, structured, and API-accessible — or does it require legal review, scraping, licensing, and transformation?
- [ ] Does the claimed technical novelty have prior art in academic literature or adjacent industries?
- [ ] Is the proposed schema or architecture genuinely novel, or is it a well-known pattern in a new domain?
- [ ] Are stated compute / storage / API cost estimates based on real provider pricing, or are they placeholder figures?
- [ ] Does the technical roadmap sequence dependencies correctly (e.g., can't build cascade alerting before the dependency graph is populated)?

### Market and Revenue Claims
- [ ] Are the cited market size figures from reputable sources with methodology disclosed?
- [ ] Is the implied market penetration rate realistic given competitive dynamics and sales cycle length?
- [ ] Are buyer personas clearly separated from each other (different pain points, different budgets, different procurement paths)?
- [ ] Does the revenue model (SaaS, transactional, API calls, licensing) match how the buyer actually budgets for this category?
- [ ] Is the ACV (Annual Contract Value) consistent with the cost of sales implied by the buyer persona?

### Timeline and Execution
- [ ] How long do enterprise sales cycles actually take in this buyer segment? (Financial services: 12–24 months)
- [ ] How long does regulatory data acquisition and normalization actually take at the claimed scope?
- [ ] Is the headcount plan consistent with the technical complexity described?
- [ ] Are compliance, legal review, and security certification timelines included?

---

## Dangerous Assumption Taxonomy

### Type A: The Invisible Prerequisite
A claim that is true only if a hard, unstated prerequisite is met.

*Example:* "We normalize regulations across jurisdictions using publicly available data."  
*Hidden prerequisite:* Regulatory text for all claimed jurisdictions is actually machine-readable, licensed for reuse, consistently structured, and available in a common language. This is false for significant portions of global regulatory text.

*Test:* For every claim beginning with "we use publicly available..." or "the data already exists...", ask: who did the work to make it usable, and what did that cost?

### Type B: The Circular Value Proposition
A feature that requires adoption to deliver value, but requires demonstrated value to achieve adoption.

*Example:* "Crowdsourced credentialed legal interpretations will provide unique value."  
*Circular dependency:* Legal professionals will only contribute if there is an audience. There is only an audience if there is content. There is no content without contributors.

*Test:* What is the cold-start solution? What does the product look like at zero contributors?

### Type C: The Dual-Use Liability Trap
A product whose primary value proposition also enables harmful use, and whose plan ignores the liability surface.

*Example:* Tax pathway optimization that finds legal structures also finds aggressive avoidance structures that regulators are actively targeting.  
*Risk:* Platform liability, regulatory scrutiny, reputational damage, potential for the product to be cited in enforcement actions.

*Test:* If the product works exactly as designed, what is the worst-case misuse scenario? Is there a plan for that scenario?

### Type D: The Expertise Acquisition Assumption
A plan that requires recruiting talent that is scarce, expensive, or institutionally constrained.

*Example:* "We will staff the contributor network with credentialed tax attorneys and regulatory specialists."  
*Reality:* Credentialed professionals who can provide reliable regulatory interpretations are (a) expensive, (b) constrained by professional liability insurance that prohibits certain types of public advisory content, (c) employed by firms that own their work product.

*Test:* Name three specific people or organizations who have committed to contributing. If none exist, this is an assumption.

### Type E: The Regulatory Capture Reversal
When a product built for compliance purposes becomes subject to regulation itself.

*Example:* A regulatory knowledge graph that provides interpretations of law could be characterized as practicing law without a license in certain jurisdictions.  
*Risk:* Cease and desist from state bar associations; requirement for disclaimers that undermine the product's value proposition.

*Test:* Has outside counsel reviewed whether the product's outputs constitute legal advice in the jurisdictions where it will operate?

### Type F: The Incumbent Awakening
Assuming incumbents will not respond to a validated market opportunity.

*Example:* "CUBE, Wolters Kluwer, and ServiceNow don't do regulation-to-regulation dependency mapping."  
*Risk:* If this feature is genuinely valuable, CUBE — which already has a regulatory knowledge graph, Microsoft partnership, and enterprise customer base — can add it faster than a startup can acquire its first 10 enterprise customers.

*Test:* What does the world look like if CUBE announces this feature 18 months from now?

---

## Clarity Scoring

For each major claim in the plan, score on two dimensions:

| Dimension | 1 (Weak) | 3 (Adequate) | 5 (Strong) |
|---|---|---|---|
| **Evidence** | Assertion only | Cited market data | Primary research or pilot data |
| **Specificity** | Vague category | Named examples | Signed LOIs or paying customers |
| **Falsifiability** | Can't be tested | Testable in principle | Has been tested |

Claims scoring below 2 on any dimension should be flagged as gaps requiring work before investor presentation.

---

## The "Stress Test" Questions
These questions should be asked of any business plan:

1. What happens if your first target enterprise customer takes 24 months to close instead of 6?
2. What happens if the credentialed contributor network fails to reach critical mass in year 1?
3. What happens if CUBE (or the nearest well-funded incumbent) launches a competing feature?
4. What happens if a regulatory body determines that provision-level legal interpretations constitute the practice of law?
5. What does the product look like at month 18 with $0 in revenue?
6. What is the minimum viable product that a paying customer would pay for today?
7. If the founder(s) left, could this business continue?
