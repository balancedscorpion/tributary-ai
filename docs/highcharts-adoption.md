# Highcharts Adoption Notes

## Scope

This repository uses Highcharts as the artifact plotting engine for stage 2 support:

- `core` module charts
- `stock` module charts
- `maps` module charts (inline geometry only)
- `gantt` module charts

Stage 2 also enables a curated extension set (for example `drilldown`, `treemap`, `sunburst`, `sankey`, `networkgraph`, `wordcloud`, `venn`, `timeline`, `xrange`, `heatmap`, `flowmap`) with dynamic on-demand loading.

## License assumptions

Highcharts is a commercial product with a non-open-source license for most use cases.

Before production use, maintainers must confirm that one of the following is true:

1. The deployment is covered by a valid Highcharts commercial license.
2. The deployment is eligible for a no-cost usage category under Highcharts terms.

If neither is true, do not ship this integration in production.

## Runtime safety model

Model-authored chart payloads are untrusted input.

Safety controls used in this codebase:

- Chart payloads are validated against a strict, versioned schema.
- Callback functions are not accepted as raw JavaScript.
- Callback behavior is selected only by allowlisted callback IDs.
- HTML rendering in tooltips is disabled unless explicitly allowlisted.

## Capability sync

Highcharts capability metadata is generated from installed package metadata:

- Generate: `npm run highcharts:sync`
- Verify: `npm run highcharts:check`

Snapshots are committed at:

- `docs/generated/highcharts-capabilities.json`
- `server/internal/chat/tools/generated/highcharts-capabilities.json`
