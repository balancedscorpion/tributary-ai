import { ExecutableTool, ToolContext } from '../types';

export const getEchartsCapabilitiesTool: ExecutableTool = {
  definition: {
    type: 'function',
    function: {
      name: 'get_echarts_capabilities',
      description: 'Return the currently supported Apache ECharts plotting capabilities, including capability_version, chart spec versions, modules, supported series types, allowed option paths, and callback IDs.',
      parameters: {
        type: 'object',
        properties: {},
        required: [],
        additionalProperties: false,
      },
    },
  },
  execute: async (_args: any, _context: ToolContext) => {
    return JSON.stringify({ error: 'get_echarts_capabilities runs server-side only.' });
  },
};

export const echartsCapabilityTools = [getEchartsCapabilitiesTool];
