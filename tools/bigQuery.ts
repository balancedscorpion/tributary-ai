import { ExecutableTool, ToolContext, BigQueryRow } from '../types';

// Helper for local BigQuery API calls (server-side, uses ADC by default).
const callBigQueryApi = async (url: string, method: string, body?: any) => {
  const response = await fetch(url, {
    method,
    headers: body ? { 'Content-Type': 'application/json' } : undefined,
    body: body ? JSON.stringify(body) : undefined
  });

  if (!response.ok) {
    const text = await response.text().catch(() => '');
    try {
      const error = text ? JSON.parse(text) : {};
      throw new Error(
        error?.error?.message
        || error?.message
        || error?.error
        || text
        || `BigQuery API Error: ${response.statusText}`
      );
    } catch {
      throw new Error(text || `BigQuery API Error: ${response.statusText}`);
    }
  }
  return response.json();
};

// --- TOOL: Run Query ---
const runQueryTool: ExecutableTool = {
  definition: {
    type: 'function',
    function: {
      name: 'run_bigquery_query',
      description: 'Execute a read-only SQL query against Google BigQuery. This tool performs a preflight (dry-run) check first; if preflight fails, you MUST rewrite the SQL and call this tool again with a corrected query. Returns a JSON object containing an array of rows on success.',
      parameters: {
        type: 'object',
        properties: {
          sql: { type: 'string', description: 'Standard SQL query to execute.' },
          description: {
            type: 'string',
            maxLength: 300,
            description: 'One-sentence plain-English summary of what this query answers. Keep it brief (max 300 characters).',
          },
          project_id: { type: 'string', description: 'GCP Project ID (optional)' },
          limit: { type: 'number', description: 'Maximum rows to return for this page (default 200, max 2500).' },
          full_data: { type: 'boolean', description: 'If true, ask the backend to provide the full rows payload for this page without preview-level truncation.' },
          page_token: { type: 'string', description: 'Opaque token returned from previous page. Include to fetch the next page.' },
        },
        required: ['sql']
      }
    }
  },
  execute: async ({ sql, project_id }: { sql: string, project_id?: string }, context: ToolContext) => {
    const pid = project_id || context.projectId;
    if (!pid) return JSON.stringify({ error: "No project ID configured" });

    try {
      const preflight = await callBigQueryApi(
        `/api/bigquery/preflight`,
        'POST',
        { project_id: pid, sql }
      );

      if (preflight?.ok !== true) {
        const msg =
          preflight?.error?.message
          || preflight?.error
          || 'BigQuery preflight failed';
        return JSON.stringify({
          error: String(msg),
          preflight_failed: true,
          rewrite_required: true,
          instructions: 'Preflight failed; rewrite the SQL query and call run_bigquery_query again. Do not show this preflight error in the UI.',
        });
      }

      const data = await callBigQueryApi(
        `/api/bigquery/query`,
        'POST',
        { project_id: pid, sql }
      );

      const rows: BigQueryRow[] = Array.isArray(data?.rows) ? data.rows : [];
      return JSON.stringify({ rows });
    } catch (e: any) {
      return JSON.stringify({ error: e.message });
    }
  }
};

// --- TOOL: List Datasets ---
const listDatasetsTool: ExecutableTool = {
  definition: {
    type: 'function',
    function: {
      name: 'list_bq_datasets',
      description: 'List all BigQuery datasets in the project. Use this to find available datasets.',
      parameters: {
        type: 'object',
        properties: {
          project_id: { type: 'string', description: 'GCP Project ID (optional)' }
        }
      }
    }
  },
  execute: async ({ project_id }: { project_id?: string }, context: ToolContext) => {
    const pid = project_id || context.projectId;
    if (!pid) return JSON.stringify({ error: "No project ID configured" });

    try {
      const data = await callBigQueryApi(
        `/api/bigquery/datasets?project_id=${encodeURIComponent(pid)}`,
        'GET'
      );
      const datasets = Array.isArray(data?.datasets) ? data.datasets : [];
      
      // WRAPPED: Return object instead of array
      return JSON.stringify({ datasets });
    } catch (e: any) {
      return JSON.stringify({ error: e.message });
    }
  }
};

// --- TOOL: List Tables ---
const listTablesTool: ExecutableTool = {
  definition: {
    type: 'function',
    function: {
      name: 'list_bq_tables',
      description: 'List all tables in a specific BigQuery dataset.',
      parameters: {
        type: 'object',
        properties: {
          dataset_id: { type: 'string', description: 'The Dataset ID' },
          project_id: { type: 'string', description: 'GCP Project ID (optional)' }
        },
        required: ['dataset_id']
      }
    }
  },
  execute: async ({ dataset_id, project_id }: { dataset_id: string, project_id?: string }, context: ToolContext) => {
    const pid = project_id || context.projectId;
    if (!pid) return JSON.stringify({ error: "No project ID configured" });

    try {
      const data = await callBigQueryApi(
        `/api/bigquery/tables?project_id=${encodeURIComponent(pid)}&dataset_id=${encodeURIComponent(dataset_id)}`,
        'GET'
      );
      const tables = Array.isArray(data?.tables) ? data.tables : [];
      
      // WRAPPED: Return object instead of array
      return JSON.stringify({ tables });
    } catch (e: any) {
      return JSON.stringify({ error: e.message });
    }
  }
};

// --- TOOL: Get Table Schema ---
const getTableSchemaTool: ExecutableTool = {
  definition: {
    type: 'function',
    function: {
      name: 'get_bq_table_schema',
      description: 'Get the schema (columns and types) of a BigQuery table. Use this before writing queries to ensure column names are correct.',
      parameters: {
        type: 'object',
        properties: {
          dataset_id: { type: 'string' },
          table_id: { type: 'string' },
          project_id: { type: 'string' }
        },
        required: ['dataset_id', 'table_id']
      }
    }
  },
  execute: async ({ dataset_id, table_id, project_id }: { dataset_id: string, table_id: string, project_id?: string }, context: ToolContext) => {
    const pid = project_id || context.projectId;
    if (!pid) return JSON.stringify({ error: "No project ID configured" });

    try {
      const data = await callBigQueryApi(
        `/api/bigquery/schema?project_id=${encodeURIComponent(pid)}&dataset_id=${encodeURIComponent(dataset_id)}&table_id=${encodeURIComponent(table_id)}`,
        'GET'
      );
      const schema = Array.isArray(data?.schema) ? data.schema : [];
      
      // WRAPPED: Return object instead of array
      return JSON.stringify({ schema });
    } catch (e: any) {
      return JSON.stringify({ error: e.message });
    }
  }
};

export const bigQueryTools = [runQueryTool, listDatasetsTool, listTablesTool, getTableSchemaTool];
