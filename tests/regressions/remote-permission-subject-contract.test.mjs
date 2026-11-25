import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

test('permission subjects preserve remote resource fields in the frontend contract', () => {
  const types = sourceAt('types/permissions.ts');
  const toolTestApi = sourceAt('services/toolTestApi.ts');

  assert.ok(types.includes('remote_kind?: string;'), 'PermissionSubject must expose remote_kind');
  assert.ok(types.includes('remote_id?: string;'), 'PermissionSubject must expose remote_id');
  assert.ok(types.includes('excludes?: string[];'), 'PermissionSubject must expose local excludes');
  assert.ok(types.includes("| 'remote_resource'"), 'PermissionSubjectKind must include remote_resource');
  assert.ok(types.includes("| 'repository'"), 'PermissionSubjectKind must include repository');
  assert.ok(
    types.includes("'remote_kind', 'remoteKind', 'resource_kind', 'resourceKind'"),
    'normalizer must accept snake/camel remote kind aliases'
  );
  assert.ok(
    types.includes("'remote_id', 'remoteId', 'resource_id', 'resourceId'"),
    'normalizer must accept snake/camel remote id aliases'
  );
  assert.ok(
    toolTestApi.includes('subject.remote_kind = remoteKind') &&
      toolTestApi.includes('subject.remote_id = remoteId'),
    'tool-test permission errors must preserve remote target fields'
  );
  assert.ok(
    toolTestApi.includes('const excludes = cleanStringArray(value.excludes);') &&
      toolTestApi.includes('subject.excludes = excludes'),
    'tool-test permission errors must preserve local-location exclude patterns'
  );
});

test('approval presentation names remote targets and always-allow scope clearly', () => {
  const presentation = sourceAt('lib/approvalPresentation.ts');
  const card = sourceAt('components/approvals/ApprovalCard.tsx');

  assert.ok(
    presentation.includes("subject?.remote_id") &&
      presentation.includes("return 'Always allow this target';"),
    'scope copy must make always-allow remote target grants explicit'
  );
  for (const label of [
    'Slack channel',
    'Slack thread',
    'Slack user',
    'Slack file',
    'GitHub repository',
    'GitHub pull request',
    'GitHub issue',
    'GitHub workflow run',
    'Jira issue',
    'Jira comment',
    'Jira project',
    'Jira attachment',
    'Jira REST path',
    'Confluence page',
    'Confluence space',
    'Confluence REST path',
    'SharePoint file',
    'arXiv paper',
    'Search document',
    'Thread',
    'BigQuery project',
    'BigQuery dataset',
    'BigQuery table',
    'Databricks catalog',
    'Databricks schema',
    'Databricks table',
    'Trino catalog',
    'Trino schema',
    'Trino table',
    'Snowflake database',
    'Snowflake schema',
    'Snowflake table',
    'query',
  ]) {
    assert.ok(presentation.includes(label), `subject presentation must include ${label}`);
  }
  assert.ok(
    card.includes("['Remote target type', approval.subject.remote_kind]") &&
      card.includes("['Remote target', approval.subject.remote_id]"),
    'approval technical details must show remote target fields'
  );
});
