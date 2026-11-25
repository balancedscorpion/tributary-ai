import { ExecutableTool } from '../types';
import { bigQueryTools } from './bigQuery';
import { csvAnalysisTools } from './csvAnalysis';
import { displayTableTool } from './dataTools';
import { echartsCapabilityTools } from './echartsCapabilities';
import { fileSystemTools } from './fileSystem';
import { highchartsCapabilityTools } from './highchartsCapabilities';

// Combine all tools into a single registry array
const allTools: ExecutableTool[] = [
  ...bigQueryTools,
  ...fileSystemTools,
  ...csvAnalysisTools,
  ...echartsCapabilityTools,
  ...highchartsCapabilityTools,
  displayTableTool
];

// Map for quick lookup during execution
export const toolRegistry = allTools.reduce((acc, tool) => {
  acc[tool.definition.function.name] = tool;
  return acc;
}, {} as Record<string, ExecutableTool>);

// Export definitions for the OpenAI API
export const toolDefinitions = allTools.map(t => t.definition);
