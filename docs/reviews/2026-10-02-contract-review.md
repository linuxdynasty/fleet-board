# Contract review, 2026-10-02 (before implementation)

Before any code was written, six reviewers each read the contracts from one angle
(agent wire, hub API, auth, goal lifecycle, proto hygiene, doc drift). A second
reviewer tried to refute each finding, and a critic looked for gaps between them.
73 findings survived and the critic added 13: **86 in total, all fixed**.

The fixes live in the protos and specs. The owner decisions that came out of the review
are in [ADR 0019](../adr/0019-pre-implementation-contract-decisions.md). The per-finding
detail (evidence, proposed fix, verifier note) is in git history at commit `9edaaf7`;
it is not kept as documentation ([ADR 0026](../adr/0026-one-home-per-fact.md)).
