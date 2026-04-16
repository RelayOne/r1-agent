// hire-from-trustplane.cedar
//
// Grants discovery + hire capability against the TrustPlane
// marketplace (STOKE-012 external agent hire path) scoped by a
// budget ceiling and a reputation floor carried on the DCT.
//
// Without this bundle applied, local-only execution remains the
// default — an agent can't accidentally rack up TrustPlane bills
// without explicit authorization.

permit (
    principal,
    action in [
        Action::"trustplane_discover",
        Action::"trustplane_get_agent_card",
        Action::"trustplane_get_reputation"
    ],
    resource
)
when {
    principal has scope_trustplane_discover &&
    principal.scope_trustplane_discover == true
};

permit (
    principal,
    action == Action::"trustplane_hire",
    resource
)
when {
    principal has scope_trustplane_hire &&
    principal.scope_trustplane_hire == true &&
    principal has max_hire_cost_usd &&
    resource has estimated_cost_usd &&
    resource.estimated_cost_usd <= principal.max_hire_cost_usd &&
    principal has min_reputation_score &&
    resource has reputation_score &&
    resource.reputation_score >= principal.min_reputation_score
};

// Payment settlement is gated separately — a successful hire
// doesn't auto-authorize settling without explicit scope. Keeps a
// compromised agent from draining the connected wallet via quick
// repeat-hires.
permit (
    principal,
    action == Action::"trustplane_pay",
    resource
)
when {
    principal has scope_trustplane_pay &&
    principal.scope_trustplane_pay == true &&
    principal has max_payment_usd &&
    resource has amount_usd &&
    resource.amount_usd <= principal.max_payment_usd
};
