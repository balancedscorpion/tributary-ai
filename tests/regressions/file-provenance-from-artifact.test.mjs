import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { pathToFileURL } from 'node:url';

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

let fileProvenancePromise = null;
async function loadFileProvenance() {
  if (!fileProvenancePromise) {
    fileProvenancePromise = (async () => {
      // fileProvenance.ts now imports the shared generated-image helpers via the
      // '@' alias, so a single-file transform would leave an unresolvable
      // import. Bundle the module (aliasing '@' to the repo root) so the
      // dependency is inlined; type-only imports (@/types) are erased.
      const esbuild = await import('esbuild');
      const result = await esbuild.build({
        entryPoints: [path.join(process.cwd(), 'lib/fileProvenance.ts')],
        bundle: true,
        format: 'esm',
        platform: 'node',
        write: false,
        alias: { '@': process.cwd() },
        logLevel: 'silent',
      });
      const file = path.join(os.tmpdir(), `file-provenance-${process.pid}-${Date.now()}.mjs`);
      fs.writeFileSync(file, result.outputFiles[0].text);
      try {
        return await import(pathToFileURL(file).href);
      } finally {
        fs.unlinkSync(file);
      }
    })();
  }
  return fileProvenancePromise;
}

function fileArtifact(overrides = {}) {
  return {
    id: 'artifact-1',
    type: 'file',
    title: 'Untitled',
    content: '',
    metadata: {},
    ...overrides,
  };
}

test('fileProvenanceFromArtifact derives SharePoint cached-file provenance from direct-open artifacts', async () => {
  const { fileProvenanceFromArtifact } = await loadFileProvenance();

  const provenance = fileProvenanceFromArtifact(fileArtifact({
    id: 'sharepoint-1',
    title: 'Q3 Roadmap.md',
    content: JSON.stringify({ content_path: 'sharepoint/downloads/q3-roadmap.md' }),
    metadata: {
      source: 'external_tool',
      tool_name: 'sharepoint_read',
      result_metadata: {
        integration: 'sharepoint',
        detected_mime_type: 'text/markdown',
        size_bytes: '4567',
        item_url: 'https://contoso.sharepoint.com/sites/streaming/Shared%20Documents/Q3%20Roadmap.md',
        server_relative_url: '/sites/streaming/Shared Documents/Q3 Roadmap.md',
        drive_item_id: 'drive-item-123',
        local_path: 'sharepoint/downloads/q3-roadmap.md',
        tool_args: {
          drive_path: 'Shared Documents/Q3 Roadmap.md',
        },
      },
    },
  }));

  assert.equal(provenance.sourceLabel, 'SharePoint');
  assert.equal(provenance.toolName, 'sharepoint_read');
  assert.equal(provenance.mimeType, 'text/markdown');
  assert.equal(provenance.byteSize, 4567);
  assert.equal(provenance.remoteUrl, 'https://contoso.sharepoint.com/sites/streaming/Shared%20Documents/Q3%20Roadmap.md');
  assert.equal(provenance.remotePath, '/sites/streaming/Shared Documents/Q3 Roadmap.md');
  assert.equal(provenance.remoteRef, 'drive-item-123');
  assert.equal(provenance.managedLocalPath, 'sharepoint/downloads/q3-roadmap.md');
  assert.equal(provenance.contentPath, 'sharepoint/downloads/q3-roadmap.md');
  assert.equal(provenance.cached, true);
});

test('fileProvenanceFromArtifact preserves Jira attachment locators and numeric IDs', async () => {
  const { fileProvenanceFromArtifact } = await loadFileProvenance();

  const provenance = fileProvenanceFromArtifact(fileArtifact({
    id: 'jira-attachment-1',
    title: 'Incident RCA.pdf',
    metadata: {
      source: 'external_tool',
      tool_name: 'jira_read_attachment',
      result_metadata: {
        file_name: 'Incident RCA.pdf',
        mime_type: 'application/pdf',
        attachmentId: 10032,
        source_url: 'https://acme.atlassian.net/secure/attachment/10032/Incident%20RCA.pdf',
        content_path: 'jira/attachments/Incident RCA.pdf',
        tool_args: {
          issueKey: 'OPS-42',
          attachmentId: 10032,
        },
      },
    },
  }));

  assert.equal(provenance.sourceLabel, 'Jira');
  assert.equal(provenance.toolName, 'jira_read_attachment');
  assert.equal(provenance.mimeType, 'application/pdf');
  assert.equal(provenance.remoteUrl, 'https://acme.atlassian.net/secure/attachment/10032/Incident%20RCA.pdf');
  assert.equal(provenance.remoteRef, '10032');
  assert.deepEqual(provenance.toolArgs, { issueKey: 'OPS-42', attachmentId: 10032 });
  assert.equal(provenance.contentPath, 'jira/attachments/Incident RCA.pdf');
});

test('fileProvenanceFromArtifact normalizes Slack file download URLs and file IDs', async () => {
  const { fileProvenanceFromArtifact } = await loadFileProvenance();

  const provenance = fileProvenanceFromArtifact(fileArtifact({
    id: 'slack-file-1',
    title: 'pipeline-export.md',
    metadata: {
      source: 'external_tool',
      tool_name: 'slack_download_file',
      result_metadata: {
        name: 'pipeline-export.md',
        mimetype: 'text/markdown',
        file_id: 'F123',
        url_private_download: 'https://files.slack.com/files-pri/T123-F123/download/pipeline-export.md',
        permalink: 'https://example.slack.com/files/F123/pipeline-export.md',
        local_path: 'slack/downloads/pipeline-export.md',
      },
    },
  }));

  assert.equal(provenance.sourceLabel, 'Slack');
  assert.equal(provenance.toolName, 'slack_download_file');
  assert.equal(provenance.mimeType, 'text/markdown');
  assert.equal(provenance.remoteUrl, 'https://files.slack.com/files-pri/T123-F123/download/pipeline-export.md');
  assert.equal(provenance.remoteRef, 'F123');
  assert.equal(provenance.managedLocalPath, 'slack/downloads/pipeline-export.md');
});

test('fileProvenanceFromArtifact normalizes arXiv PDF URLs and paper IDs', async () => {
  const { fileProvenanceFromArtifact } = await loadFileProvenance();

  const provenance = fileProvenanceFromArtifact(fileArtifact({
    id: 'arxiv-paper-1',
    title: 'Sparse Attention Survey.pdf',
    metadata: {
      source: 'external_tool',
      tool_name: 'arxiv_download_paper',
      result_metadata: {
        title: 'Sparse Attention Survey',
        file_kind: 'pdf',
        mime_type: 'application/pdf',
        arxiv_id: '2406.11231',
        pdf_url: 'https://arxiv.org/pdf/2406.11231',
        content_path: 'arxiv/downloads/2406.11231.pdf',
      },
    },
  }));

  assert.equal(provenance.sourceLabel, 'arXiv');
  assert.equal(provenance.toolName, 'arxiv_download_paper');
  assert.equal(provenance.fileKind, 'pdf');
  assert.equal(provenance.mimeType, 'application/pdf');
  assert.equal(provenance.remoteUrl, 'https://arxiv.org/pdf/2406.11231');
  assert.equal(provenance.remoteRef, '2406.11231');
  assert.equal(provenance.contentPath, 'arxiv/downloads/2406.11231.pdf');
});

test('fileArtifactMimeType and fileArtifactFileKind resolve connector-native nested metadata', async () => {
  const { fileArtifactMimeType, fileArtifactFileKind } = await loadFileProvenance();

  // Slack shape: MIME/kind only in result_metadata, title without extension.
  const slackScan = fileArtifact({
    id: 'slack-scan-1',
    title: 'Quarterly Close Scan',
    metadata: {
      source: 'external_tool',
      tool_name: 'slack_download_file',
      result_metadata: {
        kind: 'file',
        mimetype: 'application/pdf',
        filetype: 'pdf',
        file_id: 'F999',
      },
    },
  });
  assert.equal(fileArtifactMimeType(slackScan), 'application/pdf');
  assert.equal(fileArtifactFileKind(slackScan), 'pdf');

  // SharePoint/HTTP shape: content_type + file_type in result_metadata.
  const sharepointScan = fileArtifact({
    id: 'sharepoint-scan-1',
    title: 'Vendor Onboarding',
    metadata: {
      result_metadata: {
        content_type: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
        file_type: 'spreadsheet',
      },
    },
  });
  assert.equal(
    fileArtifactMimeType(sharepointScan),
    'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
  );
  assert.equal(fileArtifactFileKind(sharepointScan), 'spreadsheet');

  // JSON content is the last-resort record when metadata carries nothing.
  const contentOnly = fileArtifact({
    id: 'content-only-1',
    title: 'notes',
    content: JSON.stringify({ mime_type: 'text/markdown', file_kind: 'markdown' }),
  });
  assert.equal(fileArtifactMimeType(contentOnly), 'text/markdown');
  assert.equal(fileArtifactFileKind(contentOnly), 'markdown');
});

test('fileArtifactMimeType prefers backend-normalized top-level keys over nested connector keys', async () => {
  const { fileArtifactMimeType, fileArtifactFileKind } = await loadFileProvenance();

  const artifact = fileArtifact({
    id: 'precedence-1',
    title: 'export',
    metadata: {
      detected_mime_type: 'text/csv',
      fileKind: 'csv',
      result_metadata: {
        mimetype: 'application/octet-stream',
        filetype: 'binary',
      },
    },
  });
  assert.equal(fileArtifactMimeType(artifact), 'text/csv');
  assert.equal(fileArtifactFileKind(artifact), 'csv');
});

test('fileArtifactFileKind skips generic file placeholders instead of letting them mask a real kind', async () => {
  const { fileArtifactFileKind, fileProvenanceFromArtifact } = await loadFileProvenance();

  // Envelope `kind: 'file'` precedes the connector's file_type in key order;
  // it must be skipped, not returned and not treated as the final answer.
  const masked = fileArtifact({
    id: 'masked-kind-1',
    title: 'scan',
    metadata: {
      result_metadata: {
        kind: 'file',
        file_type: 'pdf',
      },
    },
  });
  assert.equal(fileArtifactFileKind(masked), 'pdf');
  assert.equal(fileProvenanceFromArtifact(masked).fileKind, 'pdf');

  const placeholderOnly = fileArtifact({
    id: 'placeholder-kind-1',
    title: 'scan',
    metadata: { result_metadata: { kind: 'file' } },
  });
  assert.equal(fileArtifactFileKind(placeholderOnly), '');
  assert.equal(fileProvenanceFromArtifact(placeholderOnly).fileKind, undefined);
});

test('fileProvenanceFromArtifact derives Generated provenance from a generated image artifact', async () => {
  const { fileProvenanceFromArtifact } = await loadFileProvenance();

  const provenance = fileProvenanceFromArtifact({
    id: 'gen-image-1',
    type: 'image',
    title: 'A neon city skyline at dusk',
    content: 'data:image/png;base64,iVBORw0KGgo=',
    metadata: {
      source: 'generated_image',
      generated: true,
      tool_name: 'generate_image',
      tool_call_id: 'call_img_1',
      run_id: 'run_img_1',
      provider: 'openai',
      model: 'gpt-image-2',
      prompt: 'A neon city skyline at dusk',
      revised_prompt: 'A neon-lit city skyline at dusk, cinematic',
      mimeType: 'image/png',
      width: 1536,
      height: 1024,
      sizeBytes: 2198421,
      quality: 'high',
      created_at: '2026-07-12T10:00:00.000Z',
    },
  });

  assert.equal(provenance.sourceLabel, 'Generated');
  assert.equal(provenance.source, 'generated_image');
  assert.equal(provenance.toolName, 'generate_image');
  assert.equal(provenance.toolCallId, 'call_img_1');
  assert.equal(provenance.fileKind, 'image');
  assert.equal(provenance.mimeType, 'image/png');
  assert.equal(provenance.byteSize, 2198421);
  assert.equal(provenance.downloadable, true);
  assert.equal(provenance.cached, true);
  assert.equal(provenance.title, 'A neon city skyline at dusk');
  assert.equal(provenance.createdAt, Date.parse('2026-07-12T10:00:00.000Z'));
});

test('fileProvenanceFromArtifact tolerates snake_case-only generated image metadata', async () => {
  const { fileProvenanceFromArtifact } = await loadFileProvenance();

  const provenance = fileProvenanceFromArtifact({
    id: 'gen-image-2',
    type: 'image',
    title: '',
    // No explicit source/generated flag: the generating tool name is the only
    // provenance marker, and MIME comes from the data URI.
    content: 'data:image/webp;base64,UklGRh==',
    metadata: {
      tool_name: 'generate_image',
      tool_call_id: 'call_img_2',
      revised_prompt: 'A watercolor fox in a snowy forest',
      size_bytes: 812345,
    },
  });

  assert.equal(provenance.sourceLabel, 'Generated');
  assert.equal(provenance.toolName, 'generate_image');
  assert.equal(provenance.fileKind, 'image');
  assert.equal(provenance.mimeType, 'image/webp');
  assert.equal(provenance.byteSize, 812345);
  assert.equal(provenance.downloadable, true);
  // Title falls back to the artifact id when no title/prompt is present.
  assert.equal(provenance.title, 'gen-image-2');
});

test('fileProvenanceFromArtifact ignores a plain (non-generated) image attachment', async () => {
  const { fileProvenanceFromArtifact } = await loadFileProvenance();

  // A plain pasted/attached image (no generated marker) is not a Files record
  // and must keep the null behavior for non-file artifacts.
  const provenance = fileProvenanceFromArtifact({
    id: 'plain-image-1',
    type: 'image',
    title: 'screenshot.png',
    content: 'data:image/png;base64,iVBORw0KGgo=',
    metadata: { source: 'chat_upload', mime_type: 'image/png' },
  });
  assert.equal(provenance, null);
});

test('fileProvenanceFromArtifact treats chat uploads as cached local files without remote locators', async () => {
  const { fileProvenanceFromArtifact } = await loadFileProvenance();

  const provenance = fileProvenanceFromArtifact(fileArtifact({
    id: 'upload-1',
    title: 'budget.csv',
    metadata: {
      source: 'chat_upload',
      mime_type: 'text/csv',
      size: 321,
      content_path: 'uploads/budget.csv',
      cached: true,
    },
  }), { downloadable: true });

  assert.equal(provenance.sourceLabel, 'Chat upload');
  assert.equal(provenance.mimeType, 'text/csv');
  assert.equal(provenance.byteSize, 321);
  assert.equal(provenance.contentPath, 'uploads/budget.csv');
  assert.equal(provenance.cached, true);
  assert.equal(provenance.downloadable, true);
  assert.equal(provenance.remoteUrl, '');
  assert.equal(provenance.remotePath, '');
  assert.equal(provenance.remoteRef, '');
});
