import { ExecutableTool, ToolContext } from '../types';

export const getHighchartsCapabilitiesTool: ExecutableTool = {
  definition: {
    type: 'function',
    function: {
      name: 'get_highcharts_capabilities',
      description: 'Return the currently supported Highcharts plotting capabilities, including capability_version, chart spec versions, constructors, module catalog, supported series types, allowed option paths, and callback IDs.',
      parameters: {
        type: 'object',
        properties: {},
        required: [],
        additionalProperties: false,
      },
    },
  },
  execute: async (_args: any, _context: ToolContext) => {
    return JSON.stringify({ error: 'get_highcharts_capabilities runs server-side only.' });
  },
};

export const highchartsCapabilityTools = [getHighchartsCapabilitiesTool];
