import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

function generatedArray(name) {
  const source = fs.readFileSync(path.join(process.cwd(), 'lib/generatedToolMetadata.ts'), 'utf8');
  const match = source.match(new RegExp(`export const ${name} = ([\\s\\S]*?) as const;`));
  assert.ok(match, `${name} must be exported from generatedToolMetadata.ts`);
  return JSON.parse(match[1]);
}

test('generated tool-test metadata is addressable by the Permissions tab input model', () => {
  const tools = generatedArray('GENERATED_STATIC_TOOL_METADATA');
  const automaticContexts = new Set(['artifact', 'query_job', 'search_index', 'browser']);
  const unsupportedContexts = [];
  const unsupportedInputs = [];
  const missingSampleArguments = [];

  for (const tool of tools) {
    const spec = tool.test || {};
    if (!spec.supported || spec.mode === 'none') continue;

    const requiredArguments = Array.isArray(spec.required_arguments) ? spec.required_arguments : [];
    const requiredArgumentGroups = Array.isArray(spec.required_argument_groups)
      ? spec.required_argument_groups
      : [];
    const requiredContext = Array.isArray(spec.required_context) ? spec.required_context : [];
    const sampleArguments =
      spec.sample_arguments && typeof spec.sample_arguments === 'object'
        ? spec.sample_arguments
        : {};

    for (const context of requiredContext) {
      if (
        context !== 'project' &&
        context !== 'thread' &&
        !automaticContexts.has(context) &&
        !String(context).startsWith('connection:')
      ) {
        unsupportedContexts.push(`${tool.name}:${context}`);
      }
    }

    const manualContexts = requiredContext.filter(
      (context) => context === 'project' || context === 'thread',
    );
    if (
      spec.input_required === true &&
      requiredArguments.length === 0 &&
      requiredArgumentGroups.length === 0 &&
      manualContexts.length === 0
    ) {
      unsupportedInputs.push(tool.name);
    }

    const requiredInputNames = new Set([
      ...requiredArguments,
      ...requiredArgumentGroups.flat(),
    ]);
    for (const name of requiredInputNames) {
      if (!Object.prototype.hasOwnProperty.call(sampleArguments, name)) {
        missingSampleArguments.push(`${tool.name}:${name}`);
      }
    }
  }

  assert.deepEqual(unsupportedContexts, []);
  assert.deepEqual(unsupportedInputs, []);
  assert.deepEqual(missingSampleArguments, []);
});

test('settings tool-test client can address path-unsafe generated tool IDs', () => {
  const source = fs.readFileSync(path.join(process.cwd(), 'services/toolTestApi.ts'), 'utf8');

  assert.ok(
    source.includes('const body: Record<string, unknown> = { tool_name: toolName };') &&
      source.includes("fetch('/api/tools/test'"),
    'tool-test calls must send tool_name in the JSON body instead of encoding it into the URL path'
  );
});

test('Permissions tab uses generated sample arguments for tool-test placeholders', () => {
  const source = fs.readFileSync(path.join(process.cwd(), 'components/settings/PermissionsTab.tsx'), 'utf8');

  assert.ok(
    source.includes('sample_arguments') &&
      source.includes('sampleArguments') &&
      source.includes('toolTestSampleArgumentPlaceholder'),
    'PermissionsTab must preserve and render generated sample arguments for tool-test inputs'
  );
});
