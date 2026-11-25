import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { pathToFileURL } from 'node:url';

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

let artifactDownloadPromise = null;
async function loadArtifactDownload() {
  if (!artifactDownloadPromise) {
    artifactDownloadPromise = (async () => {
      const { transformSync } = await import('esbuild');
      const dir = fs.mkdtempSync(path.join(os.tmpdir(), `artifact-download-${process.pid}-`));
      try {
        // Both artifactDownload and fileProvenance now import the shared
        // generated-image classifier; compile it standalone (its only import is
        // `import type`, erased by esbuild) and rewrite every specifier that
        // points at it to the compiled file.
        const generatedFile = path.join(dir, 'generatedImageArtifacts.mjs');
        fs.writeFileSync(
          generatedFile,
          transformSync(sourceAt('lib/generatedImageArtifacts.ts'), { loader: 'ts', format: 'esm' }).code,
        );
        const generatedSpecifier = JSON.stringify(pathToFileURL(generatedFile).href);
        const provenanceFile = path.join(dir, 'fileProvenance.mjs');
        fs.writeFileSync(
          provenanceFile,
          transformSync(sourceAt('lib/fileProvenance.ts'), { loader: 'ts', format: 'esm' })
            .code
            .replace(/["']@\/lib\/generatedImageArtifacts["']/, generatedSpecifier),
        );
        const downloadCode = transformSync(sourceAt('lib/artifactDownload.ts'), { loader: 'ts', format: 'esm' })
          .code
          .replace(/["']@\/lib\/fileProvenance["']/, JSON.stringify(pathToFileURL(provenanceFile).href))
          .replace(
            /["']@\/lib\/generatedImageArtifacts["']/,
            JSON.stringify(pathToFileURL(generatedFile).href),
          );
        const downloadFile = path.join(dir, 'artifactDownload.mjs');
        fs.writeFileSync(downloadFile, downloadCode);
        return await import(pathToFileURL(downloadFile).href);
      } finally {
        fs.rmSync(dir, { recursive: true, force: true });
      }
    })();
  }
  return artifactDownloadPromise;
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

test('getArtifactDownloadFileName appends .pdf from connector MIME nested in result_metadata', async () => {
  const { getArtifactDownloadFileName } = await loadArtifactDownload();

  const fileName = getArtifactDownloadFileName(fileArtifact({
    id: 'slack-file-1',
    title: 'Quarterly Close Scan',
    metadata: {
      source: 'external_tool',
      tool_name: 'slack_download_file',
      result_metadata: {
        mimetype: 'application/pdf',
        filetype: 'pdf',
      },
    },
  }));

  assert.equal(fileName, 'Quarterly Close Scan.pdf');
});

test('getArtifactDownloadFileName keeps normalized top-level MIME priority over nested connector keys', async () => {
  const { getArtifactDownloadFileName } = await loadArtifactDownload();

  const fileName = getArtifactDownloadFileName(fileArtifact({
    id: 'slack-file-2',
    title: 'Quarterly Close Scan',
    metadata: {
      source: 'external_tool',
      normalizedMimeType: 'text/csv',
      result_metadata: {
        mimetype: 'application/pdf',
      },
    },
  }));

  assert.equal(fileName, 'Quarterly Close Scan.csv');
});

test('getArtifactDownloadFileName does not let JSON content MIME-like fields re-type non-file artifacts', async () => {
  const { getArtifactDownloadFileName } = await loadArtifactDownload();

  const fileName = getArtifactDownloadFileName(fileArtifact({
    id: 'data-artifact-1',
    type: 'data',
    title: 'Fetch capture',
    content: JSON.stringify({ content_type: 'text/html', body: '<html></html>' }),
  }));

  assert.equal(fileName, 'Fetch capture.json');
});

function generatedImageArtifact(overrides = {}) {
  return {
    id: 'img-1',
    type: 'image',
    title: 'Sunset over mountains',
    content: 'data:image/png;base64,iVBORw0KGgo=',
    metadata: { source: 'generated_image', mimeType: 'image/png' },
    ...overrides,
  };
}

test('getArtifactDownloadFileName appends .png for a generated image whose title lacks an extension', async () => {
  const { getArtifactDownloadFileName } = await loadArtifactDownload();
  assert.equal(getArtifactDownloadFileName(generatedImageArtifact()), 'Sunset over mountains.png');
});

test('getArtifactDownloadFileName honors webp/jpeg MIME for generated images', async () => {
  const { getArtifactDownloadFileName } = await loadArtifactDownload();
  assert.equal(
    getArtifactDownloadFileName(
      generatedImageArtifact({ metadata: { source: 'generated_image', mimeType: 'image/webp' }, content: 'data:image/webp;base64,AAAA' }),
    ),
    'Sunset over mountains.webp',
  );
  assert.equal(
    getArtifactDownloadFileName(
      generatedImageArtifact({ metadata: { source: 'generated_image', mimeType: 'image/jpeg' }, content: 'data:image/jpeg;base64,AAAA' }),
    ),
    'Sunset over mountains.jpg',
  );
});

test('getArtifactDownloadFileName reads snake_case mime_type for generated images', async () => {
  const { getArtifactDownloadFileName } = await loadArtifactDownload();
  assert.equal(
    getArtifactDownloadFileName(
      generatedImageArtifact({ metadata: { source: 'image_generation', mime_type: 'image/webp' }, content: '' }),
    ),
    'Sunset over mountains.webp',
  );
});

test('getArtifactDownloadFileName falls back to .png for a generated image with no MIME signal', async () => {
  const { getArtifactDownloadFileName } = await loadArtifactDownload();
  assert.equal(
    getArtifactDownloadFileName(
      generatedImageArtifact({ metadata: { source: 'generated_image' }, content: '' }),
    ),
    'Sunset over mountains.png',
  );
});

test('isRawDownloadArtifact is true for generated images and false for plain image attachments', async () => {
  const { isRawDownloadArtifact } = await loadArtifactDownload();
  assert.equal(isRawDownloadArtifact(generatedImageArtifact()), true);
  assert.equal(
    isRawDownloadArtifact(generatedImageArtifact({ metadata: { source: 'image_attachment' } })),
    false,
  );
});
