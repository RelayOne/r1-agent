# Historical Context

This directory contains design documents and working notes from Stoke's development.
These are preserved as historical context -- they informed the implementation but may
not reflect the current state of the code.

For current architecture documentation, see [docs/architecture/](../architecture/).

## Contents

### v2-guide/

Architecture guide for the v2 governance layer (ledger, bus, supervisor, consensus loops,
node types, concern fields, skill manufacturer, snapshot, wizard, harness, bench).
Written during the design phase; the implementation may have diverged in details.

### impl-guide/

Implementation guide covering the phased build plan: skills, wizard, hub, harness
independence, wisdom, validation gates, honesty judge, bench framework.
Written as build instructions; superseded by the actual code.

### trio-audit/

Multi-perspective audit of the Stoke/Ember/Flare product suite, including security
scans, compliance reviews, and engineering assessments from 17 review personas.
