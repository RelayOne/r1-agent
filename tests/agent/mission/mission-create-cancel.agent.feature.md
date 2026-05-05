# tests/agent/mission/mission-create-cancel.agent.feature.md

<!-- TAGS: smoke, mission -->
<!-- DEPENDS: r1d-server -->

## Scenario: Create a mission and cancel it before completion

- Given a fresh r1d daemon at "stdio"
- And a session "${SESSION_ID:-s-mission-1}" is started with workdir "/tmp/agentic-mission-1"
- When the external agent calls r1.mission.create with a 3-task plan
- Then r1.mission.list reports the new mission with status "pending" or "running"
- And the mission_id is non-empty

- When the external agent calls r1.mission.cancel with mission_id "${MISSION_ID}"
- Then within 5 seconds r1.mission.get reports the mission with phase "cancelled"
- And no associated lane has status "running"
- And the cortex Workspace contains a Note with tag "mission_cancelled"

## Scenario: r1.mission.cancel is idempotent

- Given the mission "${MISSION_ID}" already cancelled
- When the external agent calls r1.mission.cancel with mission_id "${MISSION_ID}" again
- Then r1.mission.get still reports phase "cancelled"
- And no error is returned

## Tool mapping (informative)
- "r1.mission.create" -> r1.mission.create
- "r1.mission.list" -> r1.mission.list
- "r1.mission.cancel" -> r1.mission.cancel
- "r1.mission.get" -> r1.mission.get
- "no associated lane" -> r1.lanes.list
- "cortex Workspace" -> r1.cortex.notes
