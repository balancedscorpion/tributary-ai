import { ExecutableTool, ToolContext } from '../types';

export const displayTableTool: ExecutableTool = {
  definition: {
    type: 'function',
    function: {
      name: 'display_table',
      description: 'Render structured data as an interactive table and chart in the chat UI. Use this when you have generated lists, statistics, or datasets that should be presented clearly to the user. Input MUST be a valid JSON array of objects. Optional chart_spec enables Highcharts and echarts_spec enables Apache ECharts.',
      parameters: {
        type: 'object',
        properties: {
          data_json: {
            type: 'string',
            description: 'A JSON string representing an array of objects (rows). Example: "[{\\"name\\": \\"A\\", \\"value\\": 10}, ...]"'
          },
          title: {
            type: 'string',
            description: 'A short title describing the data.'
          },
          chart_spec: {
            type: 'object',
            description: 'Optional Highcharts chart specification object. Supports schema_version highcharts-plot-spec.v1 and highcharts-plot-spec.v2.',
            properties: {
              schema_version: {
                type: 'string',
                enum: ['highcharts-plot-spec.v1', 'highcharts-plot-spec.v2'],
              },
              module: {
                type: 'string',
                enum: ['core', 'stock', 'maps', 'gantt'],
              },
              modules: {
                type: 'array',
                items: {
                  type: 'string',
                },
              },
              options: {
                type: 'object',
                additionalProperties: true,
              },
              extensions: {
                type: 'object',
                properties: {
                  drilldown_async: {
                    type: 'object',
                    properties: {
                      loader_id: { type: 'string' },
                      series_by_id: {
                        type: 'object',
                        additionalProperties: true,
                      },
                      max_depth: { type: 'integer' },
                    },
                    required: ['loader_id', 'series_by_id'],
                    additionalProperties: false,
                  },
                },
                additionalProperties: false,
              },
            },
            required: ['schema_version', 'module', 'options'],
            additionalProperties: false,
          },
          chart_spec_version: {
            type: 'string',
            description: 'Optional capability_version from get_highcharts_capabilities. When provided, it must match the current capability version.'
          },
          echarts_spec: {
            type: 'object',
            description: 'Optional Apache ECharts chart specification object. Supports schema_version echarts-plot-spec.v1.',
            properties: {
              schema_version: {
                type: 'string',
                enum: ['echarts-plot-spec.v1'],
              },
              module: {
                type: 'string',
                enum: ['core', 'dataviz', 'graph', 'all'],
              },
              modules: {
                type: 'array',
                items: {
                  type: 'string',
                },
              },
              options: {
                type: 'object',
                additionalProperties: true,
              },
              extensions: {
                type: 'object',
                properties: {
                  drilldown_async: {
                    type: 'object',
                    properties: {
                      loader_id: { type: 'string' },
                      series_by_id: {
                        type: 'object',
                        additionalProperties: true,
                      },
                      max_depth: { type: 'integer' },
                    },
                    required: ['loader_id', 'series_by_id'],
                    additionalProperties: false,
                  },
                },
                additionalProperties: false,
              },
            },
            required: ['schema_version', 'module', 'options'],
            additionalProperties: false,
          },
          echarts_spec_version: {
            type: 'string',
            description: 'Optional capability_version from get_echarts_capabilities. When provided, it must match the current capability version.'
          }
        },
        required: ['data_json'],
        additionalProperties: false,
      }
    }
  },
  execute: async (
    {
      data_json,
      title,
      chart_spec,
      chart_spec_version,
      echarts_spec,
      echarts_spec_version,
    }: {
      data_json: string;
      title?: string;
      chart_spec?: Record<string, any> | string;
      chart_spec_version?: string;
      echarts_spec?: Record<string, any> | string;
      echarts_spec_version?: string;
    },
    context: ToolContext,
  ) => {
    // Validate payload before returning
    try {
      const parsed = JSON.parse(data_json);
      if (!Array.isArray(parsed)) {
        return JSON.stringify({ error: "The provided data_json is not a valid JSON array." });
      }
      if (parsed.length === 0) {
        return JSON.stringify({ error: "The provided data array is empty." });
      }
      const hasInvalidRow = parsed.some((row) => row === null || typeof row !== 'object' || Array.isArray(row));
      if (hasInvalidRow) {
        return JSON.stringify({ error: "The provided data_json must be an array of objects." });
      }
      const normalizeSpec = (raw: Record<string, any> | string | undefined, key: string): { value?: Record<string, any>; error?: string } => {
        if (raw === undefined) {
          return {};
        }
        if (raw !== null && typeof raw === 'object' && !Array.isArray(raw)) {
          return { value: raw };
        }
        if (typeof raw === 'string') {
          const trimmed = raw.trim();
          if (!trimmed) {
            return { error: `The provided ${key} must be an object when supplied.` };
          }
          try {
            const parsed = JSON.parse(trimmed);
            if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
              return { value: parsed as Record<string, any> };
            }
            return { error: `The provided ${key} JSON string must decode to an object.` };
          } catch {
            return { error: `The provided ${key} must be valid JSON when supplied as a string.` };
          }
        }
        return { error: `The provided ${key} must be an object when supplied.` };
      };
      const normalizedChartSpec = normalizeSpec(chart_spec, 'chart_spec');
      if (normalizedChartSpec.error) {
        return JSON.stringify({ error: normalizedChartSpec.error });
      }
      if (chart_spec_version !== undefined && typeof chart_spec_version !== 'string') {
        return JSON.stringify({ error: "The provided chart_spec_version must be a string when supplied." });
      }
      const normalizedEchartsSpec = normalizeSpec(echarts_spec, 'echarts_spec');
      if (normalizedEchartsSpec.error) {
        return JSON.stringify({ error: normalizedEchartsSpec.error });
      }
      if (echarts_spec_version !== undefined && typeof echarts_spec_version !== 'string') {
        return JSON.stringify({ error: "The provided echarts_spec_version must be a string when supplied." });
      }
      if (normalizedChartSpec.value !== undefined && normalizedEchartsSpec.value !== undefined) {
        return JSON.stringify({ error: "Specify only one chart spec field: chart_spec or echarts_spec." });
      }
      // Return a success message instead of echoing the data to save tokens and prevent payload bloat.
      // The UI will render the table based on the tool call arguments.
      return `Success: Table displayed with ${parsed.length} rows.`;
    } catch (e) {
      return JSON.stringify({ error: "Failed to parse data_json. Ensure it is a valid JSON string or pass an array directly." });
    }
  }
};
