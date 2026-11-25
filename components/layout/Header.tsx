import React from 'react';
import { Files, Settings2, Trash2, PanelLeftOpen, PanelLeftClose } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { Separator } from '@/components/ui/separator';
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip';
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import type { AppSettings, Artifact, ConnectionCatalogId } from '@/types';
import { getProviderBadge } from '@/lib/providerConfigRuntime';
import { getCapabilityUxFlags } from '@/components/settings/providers/capabilityUxFlags';
import SourcesControl from '@/components/sources/SourcesDialog';
import ToolAuditPill from '@/components/chat/ToolAuditPill';

interface HeaderProps {
  settings: AppSettings;
  isLoading: boolean;
  isPanelOpen: boolean;
  activeArtifact: Artifact | null;
  /** Persist the shared enabled-for-run connection state. */
  onSetConnectionEnabled: (id: ConnectionCatalogId, enabled: boolean) => void | Promise<unknown>;
  projectId: string | null;
  projectFileCount: number;
  projectName: string | null;
  onOpenPanel: () => void;
  onClosePanel: () => void;
  onClearChat: () => void;
  onOpenSettings: () => void;
  onOpenFiles: () => void;
  isFilesView?: boolean;
  onProjectLoaded: (project: { id: string; name: string; fileCount: number; source?: string; gitUrl?: string; localPath?: string; rootRelPath?: string } | null) => void;
  onSaveSettingsQuiet: (settings: AppSettings) => Promise<void> | void;
  onOpenArtifact: (artifact: Artifact) => void;
  onManageLocalComputer: () => void;
  onAskAgent?: (prompt: string) => void;
}

const Header: React.FC<HeaderProps> = ({
  settings, isLoading, isPanelOpen, activeArtifact, onSetConnectionEnabled,
  projectId, projectFileCount, projectName,
  onOpenPanel, onClosePanel, onClearChat, onOpenSettings, onOpenFiles, isFilesView, onProjectLoaded,
  onSaveSettingsQuiet, onOpenArtifact, onManageLocalComputer, onAskAgent,
}) => {
  const badge = getProviderBadge(settings);
  // With the composer target pill shipped, the header badge would be a second,
  // possibly disagreeing target affordance — the pill is canonical (plan §4.5).
  const showProviderBadge = !getCapabilityUxFlags(settings).composerTarget;

  return (
    <header className="app-header sticky top-0 z-10 flex min-w-0 max-w-full shrink-0 items-center justify-between overflow-hidden border-b border-border bg-card/80 px-3 py-3 backdrop-blur-md sm:px-4 lg:px-6">
      <div className="app-header-brand flex min-w-0 items-center gap-3">
        {activeArtifact && (
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                variant="ghost"
                size="icon"
                className="mr-1 text-primary"
                onClick={isPanelOpen ? onClosePanel : onOpenPanel}
                aria-label={isPanelOpen ? 'Hide artifact panel' : 'Show artifact panel'}
              >
                {isPanelOpen ? <PanelLeftClose size={20} /> : <PanelLeftOpen size={20} />}
              </Button>
            </TooltipTrigger>
            <TooltipContent>
              {isPanelOpen ? 'Hide artifact panel' : 'Show artifact panel'}
            </TooltipContent>
          </Tooltip>
        )}

        <div className="app-header-brand-mark flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-avatar-neutral shadow-lg shadow-black/20">
          <span className="text-xs font-bold text-white">AI</span>
        </div>
        <div className="app-header-brand-copy min-w-0">
          <h1 className="font-semibold text-lg tracking-tight text-foreground">AI Analyst</h1>
          <div className="flex items-center gap-2">
            {showProviderBadge && (
              <Badge variant="secondary" className="text-[10px] font-mono">
                {badge.providerId}:{badge.model}
              </Badge>
            )}
            {isLoading && (
              <Badge variant="secondary" className="text-[10px] bg-primary/10 text-primary border-primary/20 animate-pulse">
                Processing...
              </Badge>
            )}
          </div>
        </div>
      </div>

      <div className="app-header-actions flex min-w-0 shrink-0 items-center gap-1 sm:gap-2">
        <ToolAuditPill
          settings={settings}
          onSetConnectionEnabled={onSetConnectionEnabled}
          onOpenSettings={onOpenSettings}
        />
        <Tooltip>
          <TooltipTrigger asChild>
            <Button
              variant="ghost"
              size="icon"
              onClick={onOpenFiles}
              className={`app-header-action-button ${isFilesView ? 'bg-primary/10 text-primary hover:text-primary' : 'text-muted-foreground hover:text-foreground'}`}
              aria-label="View files"
            >
              <Files size={20} />
            </Button>
          </TooltipTrigger>
          <TooltipContent>Files</TooltipContent>
        </Tooltip>
        <Separator orientation="vertical" className="h-6 hidden sm:block" />
        <SourcesControl
          settings={settings}
          onSaveSettings={onSaveSettingsQuiet}
          currentProjectId={projectId}
          currentProjectName={projectName}
          currentFileCount={projectFileCount}
          onProjectLoaded={onProjectLoaded}
          onOpenArtifact={onOpenArtifact}
          onOpenFiles={onOpenFiles}
          onManageLocalComputer={onManageLocalComputer}
          onAskAgent={onAskAgent}
        />
        <Separator orientation="vertical" className="h-6 hidden sm:block" />
        <DropdownMenu>
          <Tooltip>
            <TooltipTrigger asChild>
              <DropdownMenuTrigger asChild>
                <Button
                  variant="ghost"
                  size="icon"
                  className="app-header-action-button text-muted-foreground hover:text-destructive"
                  aria-label="Clear chat"
                >
                  <Trash2 size={20} />
                </Button>
              </DropdownMenuTrigger>
            </TooltipTrigger>
            <TooltipContent>Clear chat</TooltipContent>
          </Tooltip>
          <DropdownMenuContent align="end">
            <DropdownMenuItem onClick={onClearChat} className="text-destructive focus:text-destructive">
              Confirm clear chat
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
        <Tooltip>
          <TooltipTrigger asChild>
            {/* aria-label differs from the tooltip text so aria-describedby
                does not echo the accessible name back to screen readers. */}
            <Button
              variant="ghost"
              size="icon"
              onClick={onOpenSettings}
              className="app-header-action-button text-muted-foreground hover:text-foreground"
              aria-label="Open settings"
            >
              <Settings2 size={20} aria-hidden="true" />
            </Button>
          </TooltipTrigger>
          <TooltipContent>Settings</TooltipContent>
        </Tooltip>
      </div>
    </header>
  );
};

export default React.memo(Header);
