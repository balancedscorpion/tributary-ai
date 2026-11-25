import { ExecutableTool, ToolContext } from '../types';

// Helper to check if file system exists
const checkFS = (context: ToolContext) => {
  if (!context.fileSystem || Object.keys(context.fileSystem).length === 0) {
    throw new Error("No files uploaded. Please upload a directory first.");
  }
  return context.fileSystem;
};

// --- TOOL: List Files ---
export const listFilesTool: ExecutableTool = {
  definition: {
    type: 'function',
    function: {
      name: 'list_files',
      description: 'List files in the current directory or subdirectory. Use this to understand the project structure.',
      parameters: {
        type: 'object',
        properties: {
          path: { 
            type: 'string', 
            description: 'Optional directory path to filter by (e.g. "src/"). Defaults to root.' 
          }
        }
      }
    }
  },
  execute: async ({ path }: { path?: string }, context: ToolContext) => {
    try {
      const fs = checkFS(context);
      const searchPath = path ? path.replace(/^\.?\//, '') : ''; // Remove leading ./ or /
      
      const files = Object.keys(fs).filter(f => f.startsWith(searchPath));
      
      // Limit results to avoid token limits
      const limitedFiles = files.slice(0, 100);
      const suffix = files.length > 100 ? `\n... (${files.length - 100} more files)` : '';

      return JSON.stringify({ 
        files: limitedFiles,
        total_count: files.length,
        note: files.length > 100 ? "Output truncated." : undefined
      });
    } catch (e: any) {
      return JSON.stringify({ error: e.message });
    }
  }
};

// --- TOOL: Read File ---
export const readFileTool: ExecutableTool = {
  definition: {
    type: 'function',
    function: {
      name: 'read_file',
      description: 'Read the contents of a specific file.',
      parameters: {
        type: 'object',
        properties: {
          path: { 
            type: 'string', 
            description: 'The relative path of the file to read (e.g. "src/App.tsx").' 
          }
        },
        required: ['path']
      }
    }
  },
  execute: async ({ path }: { path: string }, context: ToolContext) => {
    try {
      const fs = checkFS(context);
      // Normalize path
      const cleanPath = path.replace(/^\.?\//, '');
      const content = fs[cleanPath];
      
      if (content === undefined) {
        return JSON.stringify({ error: `File not found: ${path}` });
      }

      return `File: ${cleanPath}\n\n${content}`;
    } catch (e: any) {
      return JSON.stringify({ error: e.message });
    }
  }
};

// --- TOOL: Search Files (grep) ---
export const searchFilesTool: ExecutableTool = {
  definition: {
    type: 'function',
    function: {
      name: 'search_files',
      description: 'Search for a string or pattern across all files (similar to ripgrep/grep).',
      parameters: {
        type: 'object',
        properties: {
          query: { 
            type: 'string', 
            description: 'The string to search for.' 
          },
          case_sensitive: {
            type: 'boolean',
            description: 'Whether the search should be case sensitive. Default false.'
          }
        },
        required: ['query']
      }
    }
  },
  execute: async ({ query, case_sensitive }: { query: string, case_sensitive?: boolean }, context: ToolContext) => {
    try {
      const fs = checkFS(context);
      const results: Record<string, string[]> = {};
      let matchCount = 0;
      const MAX_MATCHES = 20;

      const normalize = (s: string) => case_sensitive ? s : s.toLowerCase();
      const normalizedQuery = normalize(query);

      for (const [filePath, content] of Object.entries(fs)) {
        if (matchCount >= MAX_MATCHES) break;

        const lines = content.split('\n');
        lines.forEach((line, index) => {
          if (matchCount >= MAX_MATCHES) return;
          
          if (normalize(line).includes(normalizedQuery)) {
            if (!results[filePath]) results[filePath] = [];
            results[filePath].push(`Line ${index + 1}: ${line.trim()}`);
            matchCount++;
          }
        });
      }

      if (matchCount === 0) {
        return JSON.stringify({ message: "No matches found." });
      }

      return JSON.stringify({ 
        matches: results,
        note: matchCount >= MAX_MATCHES ? "Search stopped after 20 matches." : undefined
      });
    } catch (e: any) {
      return JSON.stringify({ error: e.message });
    }
  }
};

export const fileSystemTools = [listFilesTool, readFileTool, searchFilesTool];
