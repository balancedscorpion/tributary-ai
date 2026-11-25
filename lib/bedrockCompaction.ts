export const COMPACTION_LEGACY_INSTRUCTION =
  'Focus on creating a detailed handover of where you previously left off what your core objective was, what your major findings were, and what you have remaining to solve, plus any useful context for supporting this';

export const COMPACTION_DEFAULT_INSTRUCTION =
  'Create a compact but complete handover summary. Include the latest user request, confirmed findings, key evidence (queries, jobs, artifacts, and message references), decisions made, and remaining next steps. Preserve critical context needed to continue accurately. Do not draft or continue the user-facing reply; output summary content only.';
export const COMPACTION_DEFAULT_TRIGGER_TOKENS = 170000;

export const BEDROCK_COMPACTION_DEFAULT_INSTRUCTION = COMPACTION_DEFAULT_INSTRUCTION;
export const ANTHROPIC_COMPACTION_DEFAULT_INSTRUCTION = COMPACTION_DEFAULT_INSTRUCTION;
export const BEDROCK_COMPACTION_LEGACY_INSTRUCTION = COMPACTION_LEGACY_INSTRUCTION;
export const ANTHROPIC_COMPACTION_LEGACY_INSTRUCTION = COMPACTION_LEGACY_INSTRUCTION;
export const BEDROCK_COMPACTION_DEFAULT_TRIGGER_TOKENS = COMPACTION_DEFAULT_TRIGGER_TOKENS;
export const ANTHROPIC_COMPACTION_DEFAULT_TRIGGER_TOKENS = COMPACTION_DEFAULT_TRIGGER_TOKENS;
export const OPENAI_COMPACTION_DEFAULT_TRIGGER_TOKENS = 200000;
