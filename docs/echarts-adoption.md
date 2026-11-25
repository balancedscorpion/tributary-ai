# Apache ECharts Adoption

## Scope
- Apache ECharts is integrated as an optional artifact plotting engine.
- Runtime exposure to the model is controlled by `featureFlags.chartEngines`.

## Licensing
- Dependency: `echarts` (Apache License 2.0).
- Dependency: `echarts-for-react` (MIT).
- This repository uses these packages for in-app chart rendering only.
- Teams should validate downstream distribution requirements against Apache-2.0 and MIT obligations.

## Safety Model
- Model-supplied executable JavaScript is not allowed.
- Callback fields are ID-based and resolved from a local registry.
- Unknown callback IDs and function-like payloads are rejected server-side and client-side.
