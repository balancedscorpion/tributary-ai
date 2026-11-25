# Trino Integration Ticket Pack

This folder contains execution-ready planning docs for Trino integration and the follow-up query-provider registry refactor.

## Documents

1. `TICKET_TEMPLATE.md`
- Reusable ticket template with required sections.

2. `release-a-trino-v1.md`
- Trino launch tickets (`TRINO-001` to `TRINO-015`).
- Includes dependencies, file scopes, and acceptance criteria.

3. `release-b-registry-refactor.md`
- Post-launch refactor tickets (`REG-001` to `REG-004`).
- Keeps refactor separate from Trino delivery risk.

## Usage

1. Use `TICKET_TEMPLATE.md` for new tickets.
2. Copy a ticket from Release A or B into your issue tracker.
3. Keep acceptance criteria as checklist items for sign-off.

## Sequence

1. Execute Release A (`TRINO-*`) in order of dependencies.
2. Execute Release B (`REG-*`) only after Trino is stable.
