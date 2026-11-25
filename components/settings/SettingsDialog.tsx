import React, { useEffect, useState } from 'react';
import { Settings as SettingsIcon } from 'lucide-react';
import { Button } from '@/components/ui/button';
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from '@/components/ui/dialog';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs';
import { ScrollArea } from '@/components/ui/scroll-area';
import type { AppSettings, Artifact, ConnectionCatalogId, SavedConfig } from '@/types';
import type { FileArtifactProvenance } from '@/lib/fileProvenance';
import { migrateSettings } from '@/services/settingsMigration';
import { getActivationStatus, type NormalizedActivationStatus } from '@/services/activationApi';
import { applyProviderConfigToLegacySettings } from '@/lib/providerConfigRuntime';
import ProvidersTab from './ProvidersTab';
import UsageTab from './UsageTab';
import SystemPromptTab from './SystemPromptTab';
import AdvancedTab from './AdvancedTab';
import McpIntegrationsTab from './McpIntegrationsTab';
import PermissionsTab from './PermissionsTab';
import FilesTab from './FilesTab';
import SecretsUnlockPanel from './SecretsUnlockPanel';
import ActivationPanel from './ActivationPanel';
import type { NormalizedSecretsStatus } from '@/services/secretsApi';
import type { FileAuditRecord } from '@/services/fileAuditApi';

interface SettingsDialogProps {
  isOpen: boolean;
  onClose: () => void;
  settings: AppSettings;
  onSave: (settings: AppSettings) => void | Promise<void>;
  /**
   * Serialized settings mutation (useSettings.applySettingsMutation). Scoped
   * connection persists on the Connections tab rebase onto the latest saved
   * settings through this instead of overwriting with a stale snapshot.
   */
  onApplySettingsMutation?: (mutator: (latest: AppSettings) => AppSettings) => Promise<AppSettings>;
  profiles: SavedConfig[];
  onSaveProfile: (name: string, settings: AppSettings) => void;
  onDeleteProfile: (id: string) => void;
  onLoadProfiles: () => void;
  /** Attach a file to the chat composer (standard upload path). */
  onAttachFile?: (file: File) => void | Promise<void>;
  /** Append text to the chat composer draft. */
  onInsertDraftText?: (text: string) => void;
  /** Tab to show when the dialog opens (deep-link from chat/toasts). */
  initialTab?: 'providers' | 'usage' | 'mcps' | 'permissions' | 'files' | 'system-prompt' | 'advanced';
  /** Connection whose setup window should open once the Connections tab mounts. */
  focusConnectionId?: ConnectionCatalogId | null;
  /** Provider connection whose detail view should open on the Providers tab. */
  focusProviderConfigId?: string | null;
  /** Current chat context used by Settings → Permissions tool tests. */
  toolTestContext?: {
    projectId?: string | null;
    projectName?: string | null;
    threadId?: string | null;
    threadTitle?: string | null;
  };
  onOpenArtifact?: (artifact: Artifact, provenance?: FileArtifactProvenance) => void;
  onOpenFiles?: () => void;
  onOpenOriginThread?: (threadId: string, toolCallId?: string) => void;
  /** Ask the agent to download/cache a remote-only file reference. */
  onRequestRemoteFileDownload?: (record: FileAuditRecord) => void;
}

const SettingsDialog: React.FC<SettingsDialogProps> = ({
  isOpen, onClose, settings, onSave, onApplySettingsMutation,
  profiles, onSaveProfile, onDeleteProfile, onLoadProfiles,
  onAttachFile, onInsertDraftText,
  initialTab, focusConnectionId, focusProviderConfigId, toolTestContext,
  onOpenArtifact, onOpenFiles, onOpenOriginThread, onRequestRemoteFileDownload,
}) => {
  const [localSettings, setLocalSettings] = useState<AppSettings>(settings);
  const [activeTab, setActiveTab] = useState<string>(initialTab || 'providers');
  const [isSaving, setIsSaving] = useState(false);
  const [secretsReady, setSecretsReady] = useState(false);
  const [activationOk, setActivationOk] = useState<boolean | null>(null);
  const [activationCheckSlow, setActivationCheckSlow] = useState(false);
  const [providerWizardOpen, setProviderWizardOpen] = useState(false);

  const handleActivationStatus = React.useCallback((st: NormalizedActivationStatus | null) => {
    const ok = st?.activated === true;
    setActivationOk(ok);
    if (!ok) setSecretsReady(false);
  }, []);

  const handleSecretsReady = React.useCallback((_st: NormalizedSecretsStatus) => {
    setSecretsReady(true);
  }, []);

  useEffect(() => {
    if (!isOpen) return;
    let cancelled = false;
    const slowTimer = window.setTimeout(() => {
      if (!cancelled) setActivationCheckSlow(true);
    }, 700);

    setSecretsReady(false);
    setActivationOk(null);
    setActivationCheckSlow(false);
    setProviderWizardOpen(false);
    setActiveTab(initialTab || 'providers');
    setLocalSettings(migrateSettings(settings));
    onLoadProfiles();

    (async () => {
      try {
        const st = await getActivationStatus();
        if (cancelled) return;
        handleActivationStatus(st);
      } catch {
        if (cancelled) return;
        handleActivationStatus(null);
      }
    })();

    return () => {
      cancelled = true;
      window.clearTimeout(slowTimer);
    };
  }, [isOpen]);

  const handleSave = async () => {
    setIsSaving(true);
    try {
      const next = applyProviderConfigToLegacySettings(localSettings);
      await Promise.resolve(onSave(next));
      onClose();
    } catch {
      // Keep dialog open; caller should surface the error (toast, inline, etc.).
    } finally {
      setIsSaving(false);
    }
  };

  const activated = activationOk === true;

  return (
    <Dialog open={isOpen} onOpenChange={(open) => { if (!open) onClose(); }}>
      <DialogContent className="max-w-4xl h-[90vh] max-h-[90vh] flex flex-col p-0 gap-0 overflow-hidden">
        <DialogHeader className="p-5 pb-0">
          <DialogTitle className="flex items-center gap-2 text-lg">
            <SettingsIcon size={20} className="text-primary" />
            Settings
          </DialogTitle>
          <DialogDescription className="sr-only">
            Manage providers, review token usage, configure connections, and change application settings.
          </DialogDescription>
        </DialogHeader>

        {activationOk === null ? (
          activationCheckSlow ? (
            <div className="flex-1 min-h-0 overflow-auto">
              <ActivationPanel active={isOpen} onStatus={handleActivationStatus} />
            </div>
          ) : (
            <div className="flex-1 min-h-0 flex items-center justify-center text-sm text-muted-foreground">
              Checking activation…
            </div>
          )
        ) : !activated ? (
          <div className="flex-1 min-h-0 overflow-auto">
            <ActivationPanel active={isOpen} onStatus={handleActivationStatus} />
            <div className="px-5 pb-5 text-sm text-muted-foreground">
              Activate this device to unlock the app.
            </div>
          </div>
        ) : !secretsReady ? (
          <div className="flex-1 min-h-0 overflow-auto">
            <SecretsUnlockPanel active={isOpen} onReady={handleSecretsReady} />
          </div>
        ) : (
          <Tabs value={activeTab} onValueChange={setActiveTab} className="flex-1 flex flex-col min-h-0">
            <div className="overflow-x-auto px-5 pt-2 pb-1">
              <TabsList className="w-max min-w-full justify-start">
                <TabsTrigger value="providers">Providers</TabsTrigger>
                <TabsTrigger value="usage">Usage</TabsTrigger>
                <TabsTrigger value="mcps">Connections</TabsTrigger>
                <TabsTrigger value="permissions">Permissions</TabsTrigger>
                <TabsTrigger value="files">Files</TabsTrigger>
                <TabsTrigger value="system-prompt">System Prompt</TabsTrigger>
                <TabsTrigger value="advanced">Advanced</TabsTrigger>
              </TabsList>
            </div>

            {/* Radix wraps viewport children in an inline `display: table` div,
                which lets intrinsically wide tab content (e.g. the Usage table)
                inflate and clip instead of being constrained to the pane. */}
            <ScrollArea
              className="flex-1 min-h-0 px-5 py-4"
              viewportClassName="[&>div]:!block"
              data-testid="settings-scroll-area"
            >
              <TabsContent value="providers" className="mt-0">
                <ProvidersTab
                  localSettings={localSettings}
                  setLocalSettings={setLocalSettings}
                  onSave={onSave}
                  onWizardOpenChange={setProviderWizardOpen}
                  focusConnectionId={focusProviderConfigId}
                />
              </TabsContent>
              <TabsContent value="usage" className="mt-0">
                <UsageTab active={isOpen && activeTab === 'usage'} />
              </TabsContent>
              <TabsContent value="mcps" className="mt-0">
                <McpIntegrationsTab
                  localSettings={localSettings}
                  setLocalSettings={setLocalSettings}
                  savedSettings={settings}
                  onSave={onSave}
                  onApplySettingsMutation={onApplySettingsMutation}
                  onAttachFile={onAttachFile}
                  onInsertDraftText={onInsertDraftText}
                  onCloseDialog={onClose}
                  focusConnectionId={focusConnectionId}
                />
              </TabsContent>
              <TabsContent value="permissions" className="mt-0">
                <PermissionsTab
                  settings={localSettings}
                  toolTestContext={toolTestContext}
                  onOpenConnections={() => setActiveTab('mcps')}
                />
              </TabsContent>
              <TabsContent value="files" className="mt-0">
                <FilesTab
                  currentThreadId={toolTestContext?.threadId ?? null}
                  currentThreadTitle={toolTestContext?.threadTitle ?? null}
                  onOpenArtifact={onOpenArtifact || (() => {})}
                  onCloseDialog={onClose}
                  onOpenFullView={onOpenFiles}
                  onOpenOriginThread={onOpenOriginThread}
                  onRequestRemoteDownload={onRequestRemoteFileDownload}
                />
              </TabsContent>
              <TabsContent value="system-prompt" className="mt-0">
                <SystemPromptTab localSettings={localSettings} setLocalSettings={setLocalSettings} />
              </TabsContent>
              <TabsContent value="advanced" className="mt-0">
                <AdvancedTab
                  active={isOpen}
                  localSettings={localSettings}
                  setLocalSettings={setLocalSettings}
                  onActivationStatus={handleActivationStatus}
                />
              </TabsContent>
            </ScrollArea>
          </Tabs>
        )}

        <DialogFooter className="p-5 pt-3 border-t border-border">
          {!secretsReady ? (
            <Button variant="outline" onClick={onClose} disabled={isSaving}>Close</Button>
          ) : activeTab === 'usage' ? (
            <Button variant="outline" onClick={onClose} disabled={isSaving}>Close</Button>
          ) : activeTab === 'mcps' ? (
            <div className="flex w-full items-center justify-between gap-3">
              <p className="text-xs text-muted-foreground">Connection changes apply immediately.</p>
              <Button variant="outline" onClick={onClose} disabled={isSaving}>Close</Button>
            </div>
          ) : providerWizardOpen ? (
            <div className="flex w-full items-center justify-between gap-3">
              <p className="text-xs text-muted-foreground">
                Use Save Provider in the provider wizard to persist provider changes.
              </p>
              <Button variant="outline" onClick={onClose} disabled={isSaving}>Close</Button>
            </div>
          ) : (
            <>
              <Button variant="outline" onClick={onClose} disabled={isSaving}>Cancel</Button>
              <Button onClick={handleSave} disabled={isSaving}>
                {isSaving ? 'Saving…' : 'Save'}
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
};

export default SettingsDialog;
