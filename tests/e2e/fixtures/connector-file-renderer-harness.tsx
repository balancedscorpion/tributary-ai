import React from 'react';
import { createRoot } from 'react-dom/client';
import ArtifactPanel from '@/components/ArtifactPanel';
import { TooltipProvider } from '@/components/ui/tooltip';
import { DEFAULT_SETTINGS } from '@/constants';
import type { Artifact } from '@/types';

/**
 * Connector-file renderer harness.
 *
 * Mounts ArtifactPanel with file artifacts shaped like the backend's
 * normalized connector envelopes (top-level normalized keys + connector-native
 * result_metadata + tool_presentation with renderer_strategy 'artifact_type'),
 * one per FileRenderer native preview branch that the files-audit fixture does
 * not already exercise: spreadsheet workbook previews, rendered slide decks,
 * connector-native image routing, sandboxed full HTML, and the no-preview
 * binary fallback card. The paired spec serves /api/artifacts/{id}/raw and
 * /extracted from Playwright routes, so each case proves the full
 * ArtifactPanel -> registry -> FileRenderer -> fetch pipeline.
 */

type ConnectorFileCase = {
  id: string;
  toolName: string;
  title: string;
  artifact: Artifact;
};

type BoundaryState = {
  error: Error | null;
};

class RendererErrorBoundary extends React.Component<React.PropsWithChildren<{ id: string }>, BoundaryState> {
  state: BoundaryState = { error: null };

  static getDerivedStateFromError(error: Error): BoundaryState {
    return { error };
  }

  render() {
    if (this.state.error) {
      return (
        <div data-renderer-error={this.props.id}>
          {this.state.error.message || String(this.state.error)}
        </div>
      );
    }
    return this.props.children;
  }
}

function isPlainRecord(value: unknown): value is Record<string, any> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

// Mirrors the envelope shape FilesAuditFixture fabricates from the backend
// contract: file artifacts hold preview text only, original bytes live behind
// /raw, and connector-native keys ride in nested result_metadata.
function connectorFileArtifact(
  id: string,
  title: string,
  toolName: string,
  sourceLabel: string,
  family: string,
  mimeType: string,
  extraMetadata: Record<string, any> = {},
): Artifact {
  const { result_metadata: resultMetadataOverrides, ...topLevelExtras } = extraMetadata;
  const chatUpload = sourceLabel === 'Chat upload';
  return {
    id,
    type: 'file',
    title,
    content: '',
    metadata: {
      tool_name: toolName,
      source: chatUpload ? 'chat_upload' : 'external_tool',
      externalToolFile: !chatUpload,
      external_tool_file: !chatUpload,
      mimeType,
      mime_type: mimeType,
      detectedMimeType: mimeType,
      detected_mime_type: mimeType,
      result_metadata: {
        integration: family === 'jira' ? 'atlassian' : family,
        family,
        product: family,
        source_tool_name: toolName,
        tool_name: toolName,
        kind: 'file',
        result_kind: 'file',
        native_viewer: 'file',
        renderer_strategy: 'artifact_type',
        renderer_key: 'file',
        file_capability: chatUpload ? 'attachment_read' : 'remote_file_download',
        external_tool_file: !chatUpload,
        attachment_artifact_id: id,
        file_artifact_id: id,
        ...(isPlainRecord(resultMetadataOverrides) ? resultMetadataOverrides : {}),
      },
      tool_presentation: {
        family,
        result_kind: 'file',
        renderer_strategy: 'artifact_type',
        renderer_key: 'file',
        native_viewer: 'file',
        artifact_types: ['file'],
        file_capability: chatUpload ? 'attachment_read' : 'remote_file_download',
        produces_file_artifact: true,
        promotes_native_input: true,
      },
      ...topLevelExtras,
    },
  };
}

function connectorFileCases(): ConnectorFileCase[] {
  return [
    // SharePoint workbook download with extraction sidecars: must land in
    // SpreadsheetWorkbookPreview (JSON path), not the raw-text branch.
    {
      id: 'sharepoint-xlsx',
      toolName: 'sharepoint_download',
      title: 'FY26 Budget.xlsx',
      artifact: connectorFileArtifact(
        'connector-file-sharepoint-xlsx',
        'FY26 Budget.xlsx',
        'sharepoint_download',
        'SharePoint',
        'sharepoint',
        'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
        {
          fileKind: 'spreadsheet',
          file_kind: 'spreadsheet',
          sizeBytes: 24576,
          size_bytes: 24576,
          managedLocalPath: 'sharepoint/downloads/fy26-budget.xlsx',
          tool_args: { drive_path: '/Shared Documents/Finance/FY26 Budget.xlsx' },
          extraction: {
            root_path: 'extracted',
            outputs: {
              workbook_path: 'index/workbook.md',
              sheet_preview_outputs: [
                'worksheets/Revenue/preview.json',
                'worksheets/Costs/preview.json',
              ],
            },
          },
        },
      ),
    },
    // Jira presentation attachment with rendered slide sidecars: must land in
    // the rendered slide deck branch.
    {
      id: 'jira-pptx',
      toolName: 'jira_read_attachment',
      title: 'QBR Deck.pptx',
      artifact: connectorFileArtifact(
        'connector-file-jira-pptx',
        'QBR Deck.pptx',
        'jira_read_attachment',
        'Jira',
        'jira',
        'application/vnd.openxmlformats-officedocument.presentationml.presentation',
        {
          fileKind: 'presentation',
          file_kind: 'presentation',
          sizeBytes: 48123,
          size_bytes: 48123,
          managedLocalPath: 'atlassian/downloads/jira/STREAM-9310/qbr-deck.pptx',
          tool_args: { issueKey: 'STREAM-9310', attachmentId: '20991' },
          extraction: {
            renderedSlides: ['slides/slide-1.png', 'slides/slide-2.png'],
          },
        },
      ),
    },
    // Slack shared image whose MIME/kind live only in connector-native nested
    // metadata and whose title has no extension: the raw image branch must
    // route from the nested keys alone.
    {
      id: 'slack-image',
      toolName: 'slack_download_file',
      title: 'deploy-graph',
      artifact: connectorFileArtifact(
        'connector-file-slack-image',
        'deploy-graph',
        'slack_download_file',
        'Slack',
        'slack',
        '',
        {
          sizeBytes: 70,
          size_bytes: 70,
          managedLocalPath: 'slack/downloads/F777/deploy-graph.png',
          tool_args: { channel: 'C0STREAM', file_id: 'F777' },
          result_metadata: {
            name: 'deploy-graph',
            mimetype: 'image/png',
            filetype: 'png',
            file_id: 'F777',
            url_private_download: 'https://files.slack.com/files-pri/T123-F777/download/deploy-graph.png',
          },
        },
      ),
    },
    // arXiv paper download: raw PDF branch through PdfCanvasViewer.
    {
      id: 'arxiv-pdf',
      toolName: 'arxiv_download_paper',
      title: '2406.01234.pdf',
      artifact: connectorFileArtifact(
        'connector-file-arxiv-pdf',
        '2406.01234.pdf',
        'arxiv_download_paper',
        'arXiv',
        'arxiv',
        'application/pdf',
        {
          fileKind: 'pdf',
          file_kind: 'pdf',
          managedLocalPath: 'arxiv/papers/2406.01234/paper.pdf',
          tool_args: { arxiv_id: '2406.01234' },
          source_url: 'https://arxiv.org/pdf/2406.01234',
        },
      ),
    },
    // Chat-uploaded HTML file: full sandboxed srcDoc iframe branch.
    {
      id: 'chat-upload-html',
      toolName: 'thread_file_attachment',
      title: 'release-notes.html',
      artifact: connectorFileArtifact(
        'connector-file-chat-html',
        'release-notes.html',
        'thread_file_attachment',
        'Chat upload',
        'attachments',
        'text/html',
        {
          fileKind: 'html',
          file_kind: 'html',
          isText: true,
          is_text: true,
          previewAvailable: true,
          preview_available: true,
        },
      ),
    },
    // SharePoint Word document with no extraction and no text preview: must
    // fall through every native branch to the metadata fallback card without
    // being mis-typed as text.
    {
      id: 'sharepoint-docx',
      toolName: 'sharepoint_download',
      title: 'Vendor Agreement.docx',
      artifact: connectorFileArtifact(
        'connector-file-sharepoint-docx',
        'Vendor Agreement.docx',
        'sharepoint_download',
        'SharePoint',
        'sharepoint',
        'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
        {
          fileKind: 'docx',
          file_kind: 'docx',
          isText: false,
          is_text: false,
          previewAvailable: false,
          preview_available: false,
          sizeBytes: 51733,
          size_bytes: 51733,
          managedLocalPath: 'sharepoint/downloads/vendor-agreement.docx',
          tool_args: { drive_path: '/Shared Documents/Contracts/Vendor Agreement.docx' },
        },
      ),
    },
  ];
}

export async function mountConnectorFileRendererHarness(): Promise<Array<{ id: string; toolName: string; title: string }>> {
  const rootNode = document.createElement('div');
  rootNode.id = 'connector-file-renderer-root';
  document.body.appendChild(rootNode);
  const cases = connectorFileCases();
  const root = createRoot(rootNode);
  root.render(
    <TooltipProvider>
      <div className="space-y-4">
        {cases.map((tc) => (
          <section
            key={tc.id}
            data-testid={`connector-file-case-${tc.id}`}
            data-tool-name={tc.toolName}
            style={{ height: 420, border: '1px solid #ddd', margin: 12, overflow: 'hidden', position: 'relative' }}
          >
            <RendererErrorBoundary id={tc.id}>
              <ArtifactPanel
                artifact={tc.artifact}
                settings={DEFAULT_SETTINGS}
                onClose={() => undefined}
              />
            </RendererErrorBoundary>
          </section>
        ))}
      </div>
    </TooltipProvider>,
  );
  await new Promise((resolve) => window.setTimeout(resolve, 50));
  return cases.map((tc) => ({ id: tc.id, toolName: tc.toolName, title: tc.title }));
}
