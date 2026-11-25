import React from 'react';
import { ExternalLink } from 'lucide-react';
import FilesAuditView from '@/components/files/FilesAuditView';
import { Button } from '@/components/ui/button';
import type { Artifact } from '@/types';
import type { FileArtifactProvenance } from '@/lib/fileProvenance';
import type { FileAuditRecord } from '@/services/fileAuditApi';

interface FilesTabProps {
  currentThreadId?: string | null;
  currentThreadTitle?: string | null;
  onOpenArtifact: (artifact: Artifact, provenance?: FileArtifactProvenance) => void;
  onCloseDialog?: () => void;
  onOpenFullView?: () => void;
  onOpenOriginThread?: (threadId: string, toolCallId?: string) => void;
  onRequestRemoteDownload?: (record: FileAuditRecord) => void;
}

const FilesTab: React.FC<FilesTabProps> = ({
  currentThreadId,
  currentThreadTitle,
  onOpenArtifact,
  onCloseDialog,
  onOpenFullView,
  onOpenOriginThread,
  onRequestRemoteDownload,
}) => {
  // Dismiss Settings first so the origin chat is not opened behind the modal.
  const handleOpenOriginThread = React.useCallback((threadId: string, toolCallId?: string) => {
    onCloseDialog?.();
    onOpenOriginThread?.(threadId, toolCallId);
  }, [onCloseDialog, onOpenOriginThread]);

  // Same modal rule for download requests: the inserted composer prompt
  // should be visible immediately, not hidden behind Settings.
  const handleRequestRemoteDownload = React.useCallback((record: FileAuditRecord) => {
    onCloseDialog?.();
    onRequestRemoteDownload?.(record);
  }, [onCloseDialog, onRequestRemoteDownload]);

  return (
    <div className="space-y-3" data-file-audit-tab>
      {onOpenFullView && (
        <div className="flex justify-end">
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="gap-1.5"
            onClick={() => {
              onCloseDialog?.();
              onOpenFullView();
            }}
          >
            <ExternalLink size={14} aria-hidden="true" />
            Open full view
          </Button>
        </div>
      )}
      <FilesAuditView
        currentThreadId={currentThreadId}
        currentThreadTitle={currentThreadTitle}
        onOpenArtifact={onOpenArtifact}
        onOpenOriginThread={onOpenOriginThread ? handleOpenOriginThread : undefined}
        onRequestRemoteDownload={onRequestRemoteDownload ? handleRequestRemoteDownload : undefined}
        onCloseAfterOpen={onCloseDialog}
        layout="settings"
      />
    </div>
  );
};

export default FilesTab;
