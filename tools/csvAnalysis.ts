import { ExecutableTool, ToolContext } from '../types';

// --- TOOL: Describe Dataset ---
const describeDatasetTool: ExecutableTool = {
  definition: {
    type: 'function',
    function: {
      name: 'describe_dataset',
      description: 'Profile a CSV file to get column names, inferred types, row count, null counts, numeric statistics (min, max, mean, median, stddev), string cardinality, and sample values — without loading the entire file. Use this first when working with any CSV file.',
      parameters: {
        type: 'object',
        properties: {
          path: {
            type: 'string',
            description: 'Relative path to the CSV file (e.g. "data/sales.csv").',
          },
        },
        required: ['path'],
      },
    },
  },
  execute: async (_args: any, _context: ToolContext) => {
    return JSON.stringify({ error: 'describe_dataset runs server-side only.' });
  },
};

// --- TOOL: Sample Rows ---
const sampleRowsTool: ExecutableTool = {
  definition: {
    type: 'function',
    function: {
      name: 'sample_rows',
      description: 'Return a sample of rows from a CSV file. Use sequential mode (default) to read rows from a specific offset, or random mode for a statistically representative sample across the entire file.',
      parameters: {
        type: 'object',
        properties: {
          path: {
            type: 'string',
            description: 'Relative path to the CSV file.',
          },
          count: {
            type: 'number',
            description: 'Number of rows to return (default 10, max 1000).',
          },
          offset: {
            type: 'number',
            description: 'Number of rows to skip before reading (default 0). Only used in sequential mode.',
          },
          full_data: {
            type: 'boolean',
            description: 'If true, request full rows payload for this request (up to implementation limits).',
          },
          random: {
            type: 'boolean',
            description: 'If true, use reservoir sampling for a random sample across the entire file (default false).',
          },
        },
        required: ['path'],
      },
    },
  },
  execute: async (_args: any, _context: ToolContext) => {
    return JSON.stringify({ error: 'sample_rows runs server-side only.' });
  },
};

// --- TOOL: Filter CSV ---
const filterCSVTool: ExecutableTool = {
  definition: {
    type: 'function',
    function: {
      name: 'filter_csv',
      description: 'Filter rows in a CSV file by a column condition. Streams through the file and returns matching rows up to the limit, plus total match count.',
      parameters: {
        type: 'object',
        properties: {
          path: {
            type: 'string',
            description: 'Relative path to the CSV file.',
          },
          column: {
            type: 'string',
            description: 'Column name to filter on.',
          },
          operator: {
            type: 'string',
            description: 'Filter operator: eq, neq, gt, lt, gte, lte, contains, starts_with, ends_with, is_null, not_null.',
          },
          value: {
            type: 'string',
            description: 'Value to compare against (not required for is_null/not_null).',
          },
          full_data: {
            type: 'boolean',
            description: 'If true, request full rows payload for this request (up to implementation limits).',
          },
          limit: {
            type: 'number',
            description: 'Max rows to return (default 100, max 1000).',
          },
        },
        required: ['path', 'column', 'operator'],
      },
    },
  },
  execute: async (_args: any, _context: ToolContext) => {
    return JSON.stringify({ error: 'filter_csv runs server-side only.' });
  },
};

// --- TOOL: Aggregate CSV ---
const aggregateCSVTool: ExecutableTool = {
  definition: {
    type: 'function',
    function: {
      name: 'aggregate_csv',
      description: 'Compute group-by aggregations on a CSV file in a single pass. Supports count, sum, avg, min, max, and count_distinct functions. Returns results with an optional group-by breakdown (up to 1000 groups).',
      parameters: {
        type: 'object',
        properties: {
          path: {
            type: 'string',
            description: 'Relative path to the CSV file.',
          },
          group_by: {
            type: 'array',
            items: { type: 'string' },
            description: 'Column names to group by (optional; omit for overall aggregation).',
          },
          metrics: {
            type: 'array',
            items: {
              type: 'object',
              properties: {
                column: { type: 'string', description: 'Column name to aggregate.' },
                function: { type: 'string', description: 'Aggregation function: count, sum, avg, min, max, count_distinct.' },
              },
              required: ['column', 'function'],
            },
            description: 'Array of {column, function} metrics to compute.',
          },
        },
        required: ['path', 'metrics'],
      },
    },
  },
  execute: async (_args: any, _context: ToolContext) => {
    return JSON.stringify({ error: 'aggregate_csv runs server-side only.' });
  },
};

export const csvAnalysisTools = [describeDatasetTool, sampleRowsTool, filterCSVTool, aggregateCSVTool];
